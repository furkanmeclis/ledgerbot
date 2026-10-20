// Package worker runs ledgerbot's background processing: inbound message
// understanding, reply delivery, event rules and alerts, webhook delivery,
// data deletion, the fake-world simulator and channel listeners.
//
// Scaling model: any number of identical worker replicas. Jobs are claimed
// with SKIP LOCKED (no leader); periodic jobs use River's leader election;
// polling listeners are spread with expiring leases. A per-company in-flight
// limiter snoozes jobs of a company that already uses its share of this
// process, so one busy tenant cannot starve the others.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/metrics"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/secretbox"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

// Deps are shared by all workers.
type Deps struct {
	Engine    *assistant.Engine
	Channels  *channels.Registry
	Dev       *channels.Dev
	Store     store.Store
	Box       *secretbox.Box
	Tenants   *tenant.Cache
	Directory tenant.Directory
	Queue     queue.Enqueuer
	Data      dataprovider.Provider
	Log       *slog.Logger
	// PerCompanyInflight caps concurrent jobs per company in this process.
	PerCompanyInflight int
	// SimCompanies lists companies the simulator drives.
	SimCompanies func() ([]domain.CompanyID, error)
}

// Register adds all workers to a River worker set.
func Register(ws *river.Workers, d *Deps) {
	fair := newFairness(d.PerCompanyInflight)
	river.AddWorker(ws, &inboundWorker{d: d, fair: fair})
	river.AddWorker(ws, &outboundWorker{d: d})
	river.AddWorker(ws, &eventWorker{d: d})
	river.AddWorker(ws, &webhookWorker{d: d})
	river.AddWorker(ws, &deletionWorker{d: d})
	river.AddWorker(ws, &simulateWorker{d: d})
	river.AddWorker(ws, &cleanupWorker{d: d})
}

// PeriodicJobs returns the scheduled jobs (run once cluster-wide by the leader).
func PeriodicJobs(simulate time.Duration) []*river.PeriodicJob {
	jobs := []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(10*time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return queue.CleanupArgs{}, nil
		}, &river.PeriodicJobOpts{RunOnStart: true}),
	}
	if simulate > 0 {
		jobs = append(jobs, river.NewPeriodicJob(river.PeriodicInterval(simulate), func() (river.JobArgs, *river.InsertOpts) {
			return queue.SimulateArgs{}, nil
		}, nil))
	}
	return jobs
}

// fairness limits in-flight jobs per company inside one process.
type fairness struct {
	limit int
	mu    sync.Mutex
	n     map[domain.CompanyID]int
}

func newFairness(limit int) *fairness {
	if limit <= 0 {
		limit = 4
	}
	return &fairness{limit: limit, n: map[domain.CompanyID]int{}}
}

func (f *fairness) acquire(c domain.CompanyID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[c] >= f.limit {
		return false
	}
	f.n[c]++
	return true
}

func (f *fairness) release(c domain.CompanyID) {
	f.mu.Lock()
	if f.n[c] <= 1 {
		delete(f.n, c)
	} else {
		f.n[c]--
	}
	f.mu.Unlock()
}

// --- inbound -----------------------------------------------------------------

type inboundWorker struct {
	river.WorkerDefaults[queue.InboundArgs]
	d    *Deps
	fair *fairness
}

func (w *inboundWorker) Timeout(*river.Job[queue.InboundArgs]) time.Duration { return 3 * time.Minute }

func (w *inboundWorker) Work(ctx context.Context, job *river.Job[queue.InboundArgs]) error {
	msg := job.Args.Msg
	if msg.CompanyID == "" {
		c, err := w.route(ctx, msg)
		if err != nil || c == "" {
			metrics.JobsProcessed.WithLabelValues(job.Kind, "unroutable").Inc()
			return err // nil when nobody owns the address: drop
		}
		msg.CompanyID = c
	}
	if !w.fair.acquire(msg.CompanyID) {
		metrics.JobsSnoozed.WithLabelValues(job.Kind).Inc()
		return river.JobSnooze(250 * time.Millisecond)
	}
	defer w.fair.release(msg.CompanyID)

	if job.Args.BlobID != "" {
		data, err := w.d.Store.GetBlob(ctx, job.Args.BlobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // expired or already erased
			}
			return err
		}
		if msg.Media == nil {
			msg.Media = &domain.Media{}
		}
		msg.Media.Data = data
	}
	start := time.Now()
	reply, err := w.d.Engine.Handle(ctx, msg)
	if errors.Is(err, tenant.ErrUnknownCompany) {
		metrics.JobsProcessed.WithLabelValues(job.Kind, "unknown_company").Inc()
		return nil
	}
	if err != nil {
		metrics.JobsProcessed.WithLabelValues(job.Kind, "error").Inc()
		return err
	}
	metrics.MessagesHandled.WithLabelValues(string(msg.Channel), string(msg.Kind)).Inc()
	metrics.HandleDuration.WithLabelValues(string(msg.Channel)).Observe(time.Since(start).Seconds())

	outs := make([]river.JobArgs, 0, len(reply.Texts))
	for _, t := range reply.Texts {
		outs = append(outs, queue.OutboundArgs{Out: domain.OutboundMessage{
			CompanyID: msg.CompanyID, Channel: msg.Channel, ChannelID: msg.ChannelID, To: msg.Sender, Text: t,
		}})
	}
	if err := w.d.Queue.EnqueueMany(ctx, outs); err != nil {
		return err
	}
	if job.Args.BlobID != "" {
		_ = w.d.Store.DeleteBlob(ctx, job.Args.BlobID) // raw media is not retained
	}
	metrics.JobsProcessed.WithLabelValues(job.Kind, "ok").Inc()
	return nil
}

// route resolves the company for a message that arrived on a central connection.
func (w *inboundWorker) route(ctx context.Context, msg domain.InboundMessage) (domain.CompanyID, error) {
	if w.d.Directory == nil {
		return "", nil
	}
	ids, err := w.d.Directory.Lookup(ctx, msg.Channel, msg.Sender)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	// Multi-company users: first match until "firma değiştir" lands (follow-up).
	return ids[0], nil
}

// --- outbound ------------------------------------------------------------------

type outboundWorker struct {
	river.WorkerDefaults[queue.OutboundArgs]
	d *Deps
}

func (w *outboundWorker) Timeout(*river.Job[queue.OutboundArgs]) time.Duration { return time.Minute }

func (w *outboundWorker) Work(ctx context.Context, job *river.Job[queue.OutboundArgs]) error {
	out := job.Args.Out
	drv, ok := w.d.Channels.Get(out.Channel)
	if !ok {
		return river.JobCancel(fmt.Errorf("no driver for %s", out.Channel))
	}
	var conn channels.Conn
	if out.Channel != domain.ChannelDev {
		c, err := LoadConn(ctx, w.d.Store, w.d.Box, out.CompanyID, out.Channel, out.ChannelID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return river.JobCancel(fmt.Errorf("no %s connection for %s", out.Channel, out.CompanyID))
			}
			return err
		}
		conn = c
	}
	if err := drv.Send(ctx, conn, out); err != nil {
		metrics.JobsProcessed.WithLabelValues(job.Kind, "error").Inc()
		return err
	}
	metrics.JobsProcessed.WithLabelValues(job.Kind, "ok").Inc()
	return nil
}

// LoadConn finds and decrypts a connection: by id, else the company's first
// enabled connection of that kind, else a central one.
func LoadConn(ctx context.Context, st store.Store, box *secretbox.Box, company domain.CompanyID, kind domain.ChannelKind, id string) (channels.Conn, error) {
	var c store.Connection
	var err error
	if id != "" {
		c, err = st.GetConnection(ctx, id)
	} else {
		err = store.ErrNotFound
		for _, scope := range []domain.CompanyID{company, ""} {
			list, lerr := st.ListConnections(ctx, scope)
			if lerr != nil {
				return channels.Conn{}, lerr
			}
			for _, cand := range list {
				if cand.Kind == kind && cand.Enabled && (scope != "" || cand.CompanyID == "") {
					c, err = cand, nil
					break
				}
			}
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return channels.Conn{}, err
	}
	return Decrypt(box, c)
}

// Decrypt opens a connection's config.
func Decrypt(box *secretbox.Box, c store.Connection) (channels.Conn, error) {
	cfg := c.Config
	if box != nil && len(cfg) > 0 {
		plain, err := box.Open(cfg)
		if err != nil {
			return channels.Conn{}, err
		}
		cfg = plain
	}
	if len(cfg) == 0 {
		cfg = []byte("{}")
	}
	return channels.Conn{Connection: c, Config: json.RawMessage(cfg)}, nil
}
