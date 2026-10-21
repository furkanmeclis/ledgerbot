package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/metrics"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/secretbox"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/telemetry"
)

// Listeners runs Poller drivers (Telegram long polling, IMAP) for connections
// this process holds a lease on. Leases expire after TTL; a crashed worker's
// listeners are picked up by another replica within one TTL.
type Listeners struct {
	DB       *pgxpool.Pool
	Store    store.Store
	Box      *secretbox.Box
	Channels *channels.Registry
	Queue    queue.Enqueuer
	Log      *slog.Logger
	Owner    string        // unique per process
	TTL      time.Duration // lease lifetime, e.g. 20s
	Max      int           // max listeners per process

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// Run blocks until ctx is done.
func (l *Listeners) Run(ctx context.Context) {
	if l.TTL <= 0 {
		l.TTL = 20 * time.Second
	}
	if l.Max <= 0 {
		l.Max = 1000
	}
	l.running = map[string]context.CancelFunc{}
	tick := time.NewTicker(l.TTL / 4)
	defer tick.Stop()
	for {
		l.reconcile(ctx)
		select {
		case <-ctx.Done():
			l.stopAll()
			l.release(context.Background())
			return
		case <-tick.C:
		}
	}
}

func (l *Listeners) reconcile(ctx context.Context) {
	conns, err := l.Store.ListConnections(ctx, "")
	if err != nil {
		l.Log.Warn("listeners: list connections", "err", err)
		return
	}
	// Shuffle so replicas do not all race for the same connections first.
	rand.Shuffle(len(conns), func(i, j int) { conns[i], conns[j] = conns[j], conns[i] })
	want := map[string]bool{}
	for _, c := range conns {
		drv, ok := l.Channels.Get(c.Kind)
		if !ok || !c.Enabled {
			continue
		}
		if _, ok := drv.(channels.Poller); !ok {
			continue
		}
		l.mu.Lock()
		_, mine := l.running[c.ID]
		n := len(l.running)
		l.mu.Unlock()
		if !mine && n >= l.Max {
			continue
		}
		held, err := l.acquire(ctx, c.ID)
		if err != nil {
			l.Log.Warn("listeners: lease", "conn", c.ID, "err", err)
			continue
		}
		if !held {
			if mine {
				l.stop(c.ID) // lost the lease (e.g. long GC pause): another replica owns it now
			}
			continue
		}
		want[c.ID] = true
		if !mine {
			l.start(ctx, c, drv.(channels.Poller))
		}
	}
	l.mu.Lock()
	var gone []string
	for id := range l.running {
		if !want[id] {
			gone = append(gone, id)
		}
	}
	l.mu.Unlock()
	for _, id := range gone {
		l.stop(id)
	}
	l.mu.Lock()
	metrics.LeasesHeld.Set(float64(len(l.running)))
	l.mu.Unlock()
}

// acquire takes or renews a lease atomically.
func (l *Listeners) acquire(ctx context.Context, id string) (bool, error) {
	var owner string
	err := l.DB.QueryRow(ctx, `
		INSERT INTO channel_leases (connection_id, owner, expires_at) VALUES ($1, $2, now() + $3::interval)
		ON CONFLICT (connection_id) DO UPDATE SET owner = EXCLUDED.owner, expires_at = EXCLUDED.expires_at
			WHERE channel_leases.owner = EXCLUDED.owner OR channel_leases.expires_at < now()
		RETURNING owner`, id, l.Owner, l.TTL.String()).Scan(&owner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return owner == l.Owner, nil
}

func (l *Listeners) release(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _ = l.DB.Exec(ctx, `DELETE FROM channel_leases WHERE owner = $1`, l.Owner)
}

func (l *Listeners) start(parent context.Context, c store.Connection, p channels.Poller) {
	conn, err := Decrypt(l.Box, c)
	if err != nil {
		l.Log.Warn("listeners: decrypt", "conn", c.ID, "err", err)
		return
	}
	ctx, cancel := context.WithCancel(parent)
	l.mu.Lock()
	l.running[c.ID] = cancel
	l.mu.Unlock()
	l.Log.Info("listener started", "conn", c.ID, "kind", c.Kind)
	go func() {
		defer l.stop(c.ID)
		err := p.Poll(ctx, conn, func(ctx context.Context, m domain.InboundMessage) error {
			return Accept(ctx, l.Store, l.Queue, m)
		})
		if err != nil && ctx.Err() == nil {
			l.Log.Warn("listener stopped", "conn", c.ID, "err", err)
		}
	}()
}

func (l *Listeners) stop(id string) {
	l.mu.Lock()
	cancel, ok := l.running[id]
	delete(l.running, id)
	l.mu.Unlock()
	if ok {
		cancel()
	}
}

func (l *Listeners) stopAll() {
	l.mu.Lock()
	for id, cancel := range l.running {
		cancel()
		delete(l.running, id)
	}
	l.mu.Unlock()
}

// Accept stores media as a blob and enqueues the message. Used by webhooks,
// pollers and the REST API.
func Accept(ctx context.Context, st store.Store, q queue.Enqueuer, m domain.InboundMessage) error {
	args := queue.InboundArgs{Msg: m, Trace: telemetry.Inject(ctx)}
	if m.Media != nil && len(m.Media.Data) > 0 {
		id := events.NewID("blob")
		if err := st.PutBlob(ctx, id, m.Media.Data, 24*time.Hour); err != nil {
			return err
		}
		media := *m.Media
		media.Data = nil
		args.Msg.Media = &media
		args.BlobID = id
	}
	_, err := q.Enqueue(ctx, args)
	return err
}

// marshalConfig is a helper for tests and the API.
func marshalConfig(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var _ = marshalConfig
