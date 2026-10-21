package worker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

type memQ struct {
	mu   sync.Mutex
	jobs []river.JobArgs
}

func (q *memQ) Enqueue(_ context.Context, a river.JobArgs) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, a)
	return int64(len(q.jobs)), nil
}

func (q *memQ) EnqueueMany(ctx context.Context, as []river.JobArgs) error {
	for _, a := range as {
		_, _ = q.Enqueue(ctx, a)
	}
	return nil
}

func (q *memQ) last() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs[len(q.jobs)-1].(queue.OutboundArgs).Out.Text
}

func TestCompanySwitching(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	src := &tenant.FileSource{Dir: "../../seed/companies"}
	q := &memQ{}
	w := &inboundWorker{d: &Deps{Store: store.NewMem(), Directory: src, Queue: q, Log: log, Tenants: tenant.NewCache(src, time.Minute, log)}}
	ctx := context.Background()
	msg := func(text string) domain.InboundMessage {
		return domain.InboundMessage{Channel: domain.ChannelWuzapi, Sender: "+905320000001", Kind: domain.KindText, Text: text}
	}
	c, handled, err := w.routeCentral(ctx, msg("stok toner"))
	if err != nil || handled || c != "demo" {
		t.Fatalf("default company: %s %v %v", c, handled, err)
	}
	if _, handled, _ := w.routeCentral(ctx, msg("firmalar")); !handled || !strings.Contains(q.last(), "Yılmaz Gıda") {
		t.Fatalf("list: %v %q", handled, q.last())
	}
	if _, handled, _ := w.routeCentral(ctx, msg("firma değiştir yılmaz")); !handled || !strings.Contains(q.last(), "Aktif firma: Yılmaz Gıda") {
		t.Fatalf("switch: %q", q.last())
	}
	if c, _, _ := w.routeCentral(ctx, msg("stok toner")); c != "demo2" {
		t.Fatalf("active company not used: %s", c)
	}
	if _, _, _ = w.routeCentral(ctx, msg("firma değiştir 1")); !strings.Contains(q.last(), "Demo Kırtasiye") {
		t.Fatalf("switch by number: %q", q.last())
	}
	// A single-company user never sees the switch flow.
	single := domain.InboundMessage{Channel: domain.ChannelWuzapi, Sender: "+905320000002", Kind: domain.KindText, Text: "firmalar"}
	if c, handled, _ := w.routeCentral(ctx, single); handled || c != "demo" {
		t.Fatalf("single company user: %s %v", c, handled)
	}
}
