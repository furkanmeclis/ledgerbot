package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/metrics"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

// --- events: rules -> alerts, and webhook fan-out ----------------------------

type eventWorker struct {
	river.WorkerDefaults[queue.EventArgs]
	d *Deps
}

// alertChannelOrder is the preference when a user has several addresses.
var alertChannelOrder = []domain.ChannelKind{domain.ChannelWuzapi, domain.ChannelWABA, domain.ChannelTelegram, domain.ChannelEmail, domain.ChannelDev}

func (w *eventWorker) Work(ctx context.Context, job *river.Job[queue.EventArgs]) error {
	ev := job.Args.Event
	if ev.Type == events.SettingsChanged {
		w.d.Tenants.Invalidate(ev.CompanyID)
		return nil
	}
	snap, err := w.d.Tenants.Get(ctx, ev.CompanyID)
	if errors.Is(err, tenant.ErrUnknownCompany) {
		return nil
	}
	if err != nil {
		return err
	}
	var jobs []river.JobArgs
	for _, wh := range snap.Settings.Webhooks {
		for _, pat := range wh.Events {
			if tenant.Match(pat, ev.Type) {
				jobs = append(jobs, queue.WebhookArgs{URL: wh.URL, Secret: wh.Secret, Event: ev})
				break
			}
		}
	}
	if ev.Source != "ledgerbot" {
		alerts, err := w.alerts(ctx, snap, ev)
		if err != nil {
			return err
		}
		jobs = append(jobs, alerts...)
	}
	return w.d.Queue.EnqueueMany(ctx, jobs)
}

func (w *eventWorker) alerts(ctx context.Context, snap *tenant.Snapshot, ev events.Event) ([]river.JobArgs, error) {
	text, entity := alertText(snap.Settings.Locale, ev)
	if text == "" {
		return nil, nil
	}
	conns, err := w.d.Store.ListConnections(ctx, ev.CompanyID)
	if err != nil {
		return nil, err
	}
	active := map[domain.ChannelKind]bool{domain.ChannelDev: true}
	for _, c := range conns {
		if c.Enabled {
			active[c.Kind] = true
		}
	}
	var out []river.JobArgs
	for _, rule := range snap.Settings.Rules {
		if !tenant.Match(rule.Event, ev.Type) {
			continue
		}
		ttl, _ := time.ParseDuration(rule.Throttle)
		for _, u := range snap.UsersWithRoles(rule.NotifyRoles) {
			if ttl > 0 {
				ok, err := w.d.Store.ThrottleOnce(ctx, ev.CompanyID, ev.Type+"|"+entity+"|"+string(u.ID), ttl)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
			}
			for _, ch := range alertChannelOrder {
				addr, has := u.Addresses[ch]
				if !has || !active[ch] {
					continue
				}
				out = append(out, queue.OutboundArgs{Out: domain.OutboundMessage{CompanyID: ev.CompanyID, Channel: ch, To: addr, Text: text}})
				break
			}
		}
	}
	return out, nil
}

func alertText(locale string, ev events.Event) (text, entity string) {
	lang := "tr"
	if len(locale) >= 2 && locale[:2] == "en" {
		lang = "en"
	}
	switch ev.Type {
	case events.StockLow:
		var d events.StockLowData
		if json.Unmarshal(ev.Data, &d) != nil {
			return "", ""
		}
		return assistant.T(lang, "alert_stock", d.Name, qty(d.Stock), d.Unit, qty(d.MinStock)), d.ProductID
	case events.InvoiceOverdue:
		var d events.InvoiceOverdueData
		if json.Unmarshal(ev.Data, &d) != nil {
			return "", ""
		}
		return assistant.T(lang, "alert_overdue", d.CustomerName, d.Amount.String(), d.InvoiceNo, d.DueDate), d.InvoiceNo
	case events.PaymentReceived:
		var d events.PaymentReceivedData
		if json.Unmarshal(ev.Data, &d) != nil {
			return "", ""
		}
		return "💰 " + d.CustomerID + ": " + d.Amount.String(), d.Reference
	}
	return "", ""
}

func qty(q int64) string {
	if q%1000 == 0 {
		return strconv.FormatInt(q/1000, 10)
	}
	return strconv.FormatFloat(float64(q)/1000, 'f', -1, 64)
}

// --- webhook delivery -------------------------------------------------------

type webhookWorker struct {
	river.WorkerDefaults[queue.WebhookArgs]
	d *Deps
}

var webhookClient = &http.Client{Timeout: 10 * time.Second}

// Sign returns the X-Ledgerbot-Signature header value: t=<unix>,v1=<hex hmac(secret, t + "." + body)>.
func Sign(secret string, t int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	ts := strconv.FormatInt(t, 10)
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (w *webhookWorker) Work(ctx context.Context, job *river.Job[queue.WebhookArgs]) error {
	body, err := json.Marshal(job.Args.Event)
	if err != nil {
		return river.JobCancel(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Args.URL, bytes.NewReader(body))
	if err != nil {
		return river.JobCancel(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ledgerbot-webhooks/1")
	req.Header.Set("X-Ledgerbot-Event", job.Args.Event.Type)
	req.Header.Set("X-Ledgerbot-Delivery", job.Args.Event.ID)
	req.Header.Set("X-Ledgerbot-Signature", Sign(job.Args.Secret, time.Now().Unix(), body))
	resp, err := webhookClient.Do(req)
	if err != nil {
		metrics.WebhookDeliveries.WithLabelValues("error").Inc()
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		metrics.WebhookDeliveries.WithLabelValues("rejected").Inc()
		if resp.StatusCode == http.StatusGone {
			return river.JobCancel(fmt.Errorf("subscriber gone"))
		}
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	metrics.WebhookDeliveries.WithLabelValues("ok").Inc()
	return nil
}

// --- data deletion ------------------------------------------------------------

type deletionWorker struct {
	river.WorkerDefaults[queue.DeletionArgs]
	d *Deps
}

func (w *deletionWorker) Work(ctx context.Context, job *river.Job[queue.DeletionArgs]) error {
	r, err := w.d.Store.GetDeletion(ctx, job.Args.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Status == "completed" {
		return nil
	}
	n, err := w.d.Store.EraseSubject(ctx, r)
	if err != nil {
		return err
	}
	if err := w.d.Store.CompleteDeletion(ctx, r.ID, n); err != nil {
		return err
	}
	if r.CompanyID != "" {
		ev, _ := events.New(r.CompanyID, events.DeletionDone, "ledgerbot", map[string]any{"id": r.ID, "removed": n})
		_, _ = w.d.Queue.Enqueue(ctx, queue.EventArgs{Event: ev})
	}
	w.d.Log.Info("data deletion completed", "id", r.ID, "removed", n)
	return nil
}

// --- simulator ------------------------------------------------------------------

type simulateWorker struct {
	river.WorkerDefaults[queue.SimulateArgs]
	d *Deps
}

// Work makes the fake world move: a random sale that may drop stock under the
// minimum, and an occasional overdue reminder. Real hosts emit the same events.
func (w *simulateWorker) Work(ctx context.Context, job *river.Job[queue.SimulateArgs]) error {
	companies := job.Args.Companies
	if len(companies) == 0 && w.d.SimCompanies != nil {
		var err error
		if companies, err = w.d.SimCompanies(); err != nil {
			return err
		}
	}
	var jobs []river.JobArgs
	for _, c := range companies {
		evs, err := Simulate(ctx, w.d.Data, c)
		if err != nil {
			w.d.Log.Warn("simulate failed", "company", c, "err", err)
			continue
		}
		for _, e := range evs {
			jobs = append(jobs, queue.EventArgs{Event: e})
		}
	}
	return w.d.Queue.EnqueueMany(ctx, jobs)
}

// Simulate produces one round of fake events for a company.
func Simulate(ctx context.Context, data dataprovider.Provider, c domain.CompanyID) ([]events.Event, error) {
	var out []events.Event
	prods, err := data.FindProducts(ctx, c, "", 0)
	if err != nil || len(prods) == 0 {
		return nil, err
	}
	custs, err := data.FindCustomers(ctx, c, "", 0)
	if err != nil || len(custs) == 0 {
		return nil, err
	}
	p := prods[rand.IntN(len(prods))]
	cu := custs[rand.IntN(len(custs))]
	q := int64(5+rand.IntN(30)) * 1000
	net := domain.Money(int64(p.Price) * q / 1000)
	total := net + domain.Money(int64(net)*int64(p.VATRate)/100)
	draft := events.NewID("sim")
	if _, err := data.Commit(ctx, c, dataprovider.Record{
		Kind: dataprovider.RecordOrder, CustomerID: cu.ID, Amount: total, Date: time.Now(), DraftID: draft,
		Lines: []dataprovider.Line{{ProductID: p.ID, Name: p.Name, Quantity: q, Unit: p.Unit, UnitPrice: p.Price, VATRate: p.VATRate, Total: total}},
	}); err != nil {
		return nil, err
	}
	if e, err := events.New(c, events.OrderCreated, "simulator", map[string]any{"customer_id": cu.ID, "product_id": p.ID, "quantity": q, "total": total}); err == nil {
		out = append(out, e)
	}
	low, err := data.LowStock(ctx, c)
	if err != nil {
		return out, err
	}
	for _, lp := range low {
		if e, err := events.New(c, events.StockLow, "simulator", events.StockLowData{ProductID: lp.ID, Name: lp.Name, Unit: lp.Unit, Stock: lp.Stock, MinStock: lp.MinStock}); err == nil {
			out = append(out, e)
		}
	}
	if rand.IntN(4) == 0 {
		if od, err := data.Overdue(ctx, c, 0); err == nil && len(od) > 0 {
			it := od[rand.IntN(len(od))]
			if e, err := events.New(c, events.InvoiceOverdue, "simulator", events.InvoiceOverdueData{
				CustomerID: it.CustomerID, CustomerName: it.CustomerName, InvoiceNo: it.InvoiceNo, Amount: it.Amount, DueDate: it.DueDate.Format("02.01.2006"),
			}); err == nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// --- cleanup --------------------------------------------------------------------

type cleanupWorker struct {
	river.WorkerDefaults[queue.CleanupArgs]
	d *Deps
}

func (w *cleanupWorker) Work(ctx context.Context, _ *river.Job[queue.CleanupArgs]) error {
	n, err := w.d.Store.Cleanup(ctx)
	if err == nil && n > 0 {
		w.d.Log.Info("cleanup", "removed", n)
	}
	return err
}
