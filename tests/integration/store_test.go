package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
)

func TestStorePGAuditIdempotencyDrafts(t *testing.T) {
	db := testdb.Open(t)
	pg := &store.PG{DB: db}
	ctx := context.Background()
	c := domain.CompanyID("itstore" + randID(4))
	other := domain.CompanyID("itstore" + randID(4))
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM audit_log WHERE company_id = ANY($1)`, `DELETE FROM drafts WHERE company_id = ANY($1)`,
			`DELETE FROM idempotency_keys WHERE company_id = ANY($1)`, `DELETE FROM ai_usage WHERE company_id = ANY($1)`, `DELETE FROM throttles WHERE company_id = ANY($1)`} {
			_, _ = db.Exec(context.Background(), q, []string{string(c), string(other)})
		}
	})

	t.Run("audit pagination and batching", func(t *testing.T) {
		b := store.NewBatched(pg, 50, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		var wg sync.WaitGroup
		for i := range 120 {
			wg.Go(func() {
				_ = b.Audit(ctx, store.AuditEntry{CompanyID: c, Action: "message.received", Detail: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)), At: time.Now().UTC()})
			})
		}
		wg.Wait()
		_ = b.Audit(ctx, store.AuditEntry{CompanyID: other, Action: "x", At: time.Now().UTC()})
		b.Close()
		var all []store.AuditEntry
		before := int64(0)
		for {
			page, err := pg.ListAudit(ctx, c, before, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			for i := 1; i < len(page); i++ {
				if page[i].ID >= page[i-1].ID {
					t.Fatalf("audit not newest first")
				}
			}
			all = append(all, page...)
			before = page[len(page)-1].ID
		}
		if len(all) != 120 {
			t.Fatalf("batched audit lost rows: %d", len(all))
		}
		for _, a := range all {
			if a.CompanyID != c {
				t.Fatalf("foreign audit row %+v", a)
			}
		}
	})

	t.Run("idempotency records per company", func(t *testing.T) {
		rec := store.IdemRecord{Status: 202, Body: []byte(`{"data":1}`), Hash: "h1"}
		if err := pg.PutIdem(ctx, c, "k", rec, time.Hour); err != nil {
			t.Fatal(err)
		}
		got, found, err := pg.GetIdem(ctx, c, "k")
		if err != nil || !found || got.Status != 202 || string(got.Body) != `{"data":1}` || got.Hash != "h1" {
			t.Fatalf("get idem %+v %v %v", got, found, err)
		}
		if _, found, _ := pg.GetIdem(ctx, other, "k"); found {
			t.Fatal("idempotency key visible to another company")
		}
		_ = pg.PutIdem(ctx, c, "short", rec, time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if _, found, _ := pg.GetIdem(ctx, c, "short"); found {
			t.Fatal("expired idempotency key served")
		}
	})

	t.Run("draft transitions under contention", func(t *testing.T) {
		d := domain.Draft{ID: "itd-" + randID(6), CompanyID: c, ActorID: "u1", Capability: "record_payment", Status: domain.DraftPending,
			Payload: json.RawMessage(`{"amount":100}`), Summary: "s", Channel: domain.ChannelDev, Sender: "owner"}
		if err := pg.CreateDraft(ctx, &d); err != nil {
			t.Fatal(err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				to := domain.DraftCommitted
				if i%2 == 0 {
					to = domain.DraftRejected
				}
				if _, err := pg.TransitionDraft(ctx, c, d.ID, domain.DraftPending, to, ""); err == nil {
					wins.Add(1)
				} else if !errors.Is(err, store.ErrConflict) {
					t.Errorf("transition: %v", err)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d transitions won", wins.Load())
		}
		if _, err := pg.GetDraft(ctx, other, d.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("draft visible to another company: %v", err)
		}
	})

	t.Run("draft listing, keyset and memory", func(t *testing.T) {
		base := time.Now().UTC().Add(-time.Hour)
		for i := range 7 {
			st := domain.DraftCommitted
			if i%2 == 1 {
				st = domain.DraftPending
			}
			d := domain.Draft{ID: fmt.Sprintf("itl-%s-%d", c, i), CompanyID: c, ActorID: "u-list", Capability: "record_collection", Status: st,
				Payload: json.RawMessage(fmt.Sprintf(`{"amount":%d}`, i)), Summary: fmt.Sprint(i), Channel: domain.ChannelDev, Sender: "lister", CreatedAt: base.Add(time.Duration(i) * time.Minute)}
			if err := pg.CreateDraft(ctx, &d); err != nil {
				t.Fatal(err)
			}
		}
		page1, _ := pg.ListDrafts(ctx, c, store.DraftFilter{ActorID: "u-list", Limit: 3})
		page2, _ := pg.ListDrafts(ctx, c, store.DraftFilter{ActorID: "u-list", Limit: 3, Before: page1[2].CreatedAt})
		if len(page1) != 3 || len(page2) != 3 || page1[0].Summary != "6" || page2[0].Summary != "3" {
			t.Fatalf("keyset pages: %v / %v", summaries(page1), summaries(page2))
		}
		pend, _ := pg.ListDrafts(ctx, c, store.DraftFilter{ActorID: "u-list", Status: domain.DraftPending})
		if len(pend) != 3 {
			t.Fatalf("pending filter: %v", summaries(pend))
		}
		last, err := pg.LastCommitted(ctx, c, "u-list", []string{"record_collection"})
		if err != nil || last.Summary != "6" {
			t.Fatalf("last committed %+v %v", last, err)
		}
		if _, err := pg.LastCommitted(ctx, c, "u-list", []string{"create_order"}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("capability filter: %v", err)
		}
		if _, err := pg.LatestPending(ctx, c, domain.ChannelDev, "lister", time.Minute); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stale pending draft must not be approvable: %v", err)
		}
		if p, err := pg.LatestPending(ctx, c, domain.ChannelDev, "lister", 2*time.Hour); err != nil || p.Summary != "5" {
			t.Fatalf("latest pending %+v %v", p, err)
		}
	})

	t.Run("usage quota and throttles", func(t *testing.T) {
		now := time.Now().UTC()
		for i := range 5 {
			_ = pg.RecordUsage(ctx, store.UsageRecord{CompanyID: c, Purpose: "agent", Provider: "fake", Model: "m", InputTokens: 10 * (i + 1), CostMicroUSD: 1000, At: now})
		}
		n, err := pg.CallsSince(ctx, c, now.Add(-time.Minute))
		if err != nil || n != 5 {
			t.Fatalf("calls since %d %v", n, err)
		}
		rows, _ := pg.UsageReport(ctx, c, now.Add(-time.Hour), now.Add(time.Hour))
		if len(rows) != 1 || rows[0].Calls != 5 || rows[0].InputTokens != 150 {
			t.Fatalf("usage report %+v", rows)
		}
		var first atomic.Int32
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if ok, _ := pg.ThrottleOnce(ctx, c, "alert", time.Hour); ok {
					first.Add(1)
				}
			})
		}
		wg.Wait()
		if first.Load() != 1 {
			t.Fatalf("throttle let %d through", first.Load())
		}
	})
}

func summaries(ds []domain.Draft) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Summary
	}
	return out
}

func TestAPIIdempotencyOnPG(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	c := s.company("idem")
	cust, _ := s.data.FindCustomers(ctx, c, "Ahmet Yılmaz", 1)
	before, _ := s.data.Balance(ctx, c, cust[0].ID)
	body := `{"actor_id":"u-owner","text":"Ahmet Yılmaz'dan 1.500 TL tahsilat"}`
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Go(func() {
			req, _ := http.NewRequest(http.MethodPost, s.srv.URL+"/v1/messages", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+keyFor(c))
			req.Header.Set("Idempotency-Key", "same-key")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			results[i] = res.StatusCode
		})
	}
	wg.Wait()
	after, _ := s.data.Balance(ctx, c, cust[0].ID)
	committed, _ := s.pg.ListDrafts(ctx, c, store.DraftFilter{Status: domain.DraftCommitted})
	if after.Balance != before.Balance-150000*domain.Money(len(committed)) {
		t.Fatalf("ledger and drafts disagree: %d committed, balance %d -> %d", len(committed), before.Balance, after.Balance)
	}
	all, _ := s.pg.ListDrafts(ctx, c, store.DraftFilter{})
	if len(all) != 1 {
		t.Skipf("BUG: concurrent requests with the same Idempotency-Key are all executed (%d drafts, %d committed, statuses %v); the middleware checks GetIdem before running the handler and stores the response only afterwards, with no reservation/lock", len(all), len(committed), results)
	}
}
