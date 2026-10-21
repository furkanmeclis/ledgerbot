package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
)

type insertOpter interface {
	river.JobArgs
	InsertOpts() river.InsertOpts
}

func TestJobArgsKindsAndOpts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args     insertOpter
		kind     string
		queue    string
		attempts int
	}{
		{InboundArgs{}, "inbound_message", QueueInbound, 5},
		{OutboundArgs{}, "outbound_message", QueueOutbound, 8},
		{EventArgs{}, "event", QueueEvents, 5},
		{WebhookArgs{}, "webhook_delivery", QueueWebhooks, 12},
		{DeletionArgs{}, "data_deletion", QueueMaint, 10},
		{SimulateArgs{}, "simulate", QueueMaint, 1},
		{RAGIndexArgs{}, "rag_index", QueueEvents, 6},
		{ReportsTickArgs{}, "reports_tick", QueueMaint, 1},
		{CleanupArgs{}, "cleanup", QueueMaint, 1},
	}
	kinds := map[string]bool{}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			t.Parallel()
			if got := c.args.Kind(); got != c.kind {
				t.Fatalf("Kind = %q, want %q", got, c.kind)
			}
			o := c.args.InsertOpts()
			if o.Queue != c.queue || o.MaxAttempts != c.attempts {
				t.Fatalf("opts = %+v, want queue %s attempts %d", o, c.queue, c.attempts)
			}
		})
		if kinds[c.kind] {
			t.Fatalf("duplicate kind %s", c.kind)
		}
		kinds[c.kind] = true
	}
}

func TestInboundIsUniqueByArgs(t *testing.T) {
	t.Parallel()
	o := InboundArgs{}.InsertOpts()
	if !o.UniqueOpts.ByArgs || o.UniqueOpts.ByPeriod != 24*time.Hour {
		t.Fatalf("inbound unique opts = %+v", o.UniqueOpts)
	}
	for _, a := range []insertOpter{OutboundArgs{}, EventArgs{}, WebhookArgs{}} {
		if u := a.InsertOpts().UniqueOpts; u.ByArgs || u.ByPeriod != 0 {
			t.Fatalf("%s should not be unique", a.Kind())
		}
	}
}

func TestArgsJSON(t *testing.T) {
	t.Parallel()
	e := events.Event{ID: "evt_1", Type: events.DraftCreated, CompanyID: "acme", Data: json.RawMessage(`{"a":1}`)}
	cases := []struct {
		name string
		args any
		want string
	}{
		{"deletion", DeletionArgs{ID: "del_1"}, `{"id":"del_1"}`},
		{"simulate empty", SimulateArgs{}, `{}`},
		{"simulate companies", SimulateArgs{Companies: []domain.CompanyID{"a", "b"}}, `{"companies":["a","b"]}`},
		{"rag", RAGIndexArgs{CompanyID: "acme", Source: "note", Ref: "n1", Text: "kira"}, `{"company_id":"acme","source":"note","ref":"n1","text":"kira"}`},
		{"reports", ReportsTickArgs{}, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(c.args)
			if err != nil || string(b) != c.want {
				t.Fatalf("json = %s, %v; want %s", b, err, c.want)
			}
		})
	}
	b, err := json.Marshal(WebhookArgs{URL: "https://h/x", Secret: "s", Event: e})
	if err != nil {
		t.Fatal(err)
	}
	var back WebhookArgs
	if err := json.Unmarshal(b, &back); err != nil || back.URL != "https://h/x" || back.Event.ID != "evt_1" || string(back.Event.Data) != `{"a":1}` {
		t.Fatalf("webhook round trip = %+v, %v", back, err)
	}
	b, _ = json.Marshal(InboundArgs{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["blob_id"]; ok {
		t.Fatalf("empty blob_id should be omitted: %s", b)
	}
}

func TestQueues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in                     int
		inbound, events, maint int
	}{
		{64, 64, 32, 2},
		{1, 1, 1, 2},
		{3, 3, 1, 2},
		{0, 1, 1, 2},
		{-10, 1, 1, 2},
	}
	for _, c := range cases {
		q := Queues(c.in)
		if len(q) != 5 {
			t.Fatalf("Queues(%d) has %d queues", c.in, len(q))
		}
		if q[QueueInbound].MaxWorkers != c.inbound || q[QueueOutbound].MaxWorkers != c.inbound || q[QueueWebhooks].MaxWorkers != c.inbound ||
			q[QueueEvents].MaxWorkers != c.events || q[QueueMaint].MaxWorkers != c.maint {
			t.Fatalf("Queues(%d) = %+v", c.in, q)
		}
	}
}

type fakeEnqueuer struct {
	got []river.JobArgs
	err error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, a river.JobArgs) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.got = append(f.got, a)
	return int64(len(f.got)), nil
}

func (f *fakeEnqueuer) EnqueueMany(ctx context.Context, as []river.JobArgs) error {
	for _, a := range as {
		if _, err := f.Enqueue(ctx, a); err != nil {
			return err
		}
	}
	return nil
}

func TestPublisher(t *testing.T) {
	t.Parallel()
	var _ events.Publisher = Publisher{}
	q := &fakeEnqueuer{}
	p := Publisher{Q: q}
	e := events.Event{ID: "evt_x", Type: events.StockLow}
	if err := p.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if len(q.got) != 1 {
		t.Fatalf("enqueued %d jobs", len(q.got))
	}
	ea, ok := q.got[0].(EventArgs)
	if !ok || ea.Event.ID != "evt_x" || ea.Kind() != "event" {
		t.Fatalf("enqueued %#v", q.got[0])
	}
	boom := errors.New("boom")
	if err := (Publisher{Q: &fakeEnqueuer{err: boom}}).Publish(context.Background(), e); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestRiverEnqueueManyEmptyIsNoop(t *testing.T) {
	t.Parallel()
	// A nil client would panic if touched; empty input must short-circuit.
	if err := (River{}).EnqueueMany(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := (River{}).EnqueueMany(context.Background(), []river.JobArgs{}); err != nil {
		t.Fatal(err)
	}
}
