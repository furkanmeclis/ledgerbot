package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/sign"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

func TestRiverInboundRoundTrip(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	c := s.company("rt")

	t.Run("text message to dev outbox", func(t *testing.T) {
		s.async(c, "owner", "kritik stok")
		s.waitOutbox(c, "owner", "ritik")
	})
	t.Run("write committed through the PG ledger", func(t *testing.T) {
		cust, _ := s.data.FindCustomers(ctx, c, "Ahmet Yılmaz", 1)
		before, _ := s.data.Balance(ctx, c, cust[0].ID)
		s.async(c, "owner", "Ahmet Yılmaz'dan 2.500 TL tahsilat")
		s.waitOutbox(c, "owner", "Kaydedildi: Tahsilat: Ahmet Yılmaz, 2.500,00 TL")
		after, _ := s.data.Balance(ctx, c, cust[0].ID)
		if after.Balance != before.Balance-250000 {
			t.Fatalf("balance %d -> %d", before.Balance, after.Balance)
		}
		list, _ := s.pg.ListDrafts(ctx, c, store.DraftFilter{Status: domain.DraftCommitted})
		if len(list) != 1 || list[0].ExternalID == "" {
			t.Fatalf("committed drafts %+v", list)
		}
	})
	t.Run("ask approval across two queued messages", func(t *testing.T) {
		s.async(c, "accountant", "Ahmet Yılmaz'dan 700 TL tahsilat")
		s.waitOutbox(c, "accountant", "Onaylıyor musunuz")
		s.async(c, "accountant", "evet")
		s.waitOutbox(c, "accountant", "Kaydedildi: Tahsilat: Ahmet Yılmaz, 700,00 TL")
	})
	t.Run("voice note blob is consumed and erased", func(t *testing.T) {
		audio := base64.StdEncoding.EncodeToString([]byte("gün sonu özeti"))
		r := s.call(c, http.MethodPost, "/v1/dev/messages", map[string]any{"sender": "owner", "async": true, "media": map[string]any{"mime": "audio/ogg", "data": audio}})
		if r.Status != http.StatusAccepted {
			t.Fatalf("async voice: %d %s", r.Status, r.Raw)
		}
		s.waitOutbox(c, "owner", "🎙️ \"gün sonu özeti\"")
		s.waitOutbox(c, "owner", "Tahsilat: 3.200,00 TL")
		var blobs []string
		waitFor(t, 10*time.Second, "media job completion", func() (bool, string) {
			blobs = nil
			for _, j := range s.jobs(rivertype.JobStateCompleted) {
				var a queue.InboundArgs
				if j.Kind == "inbound_message" && json.Unmarshal(j.EncodedArgs, &a) == nil && a.BlobID != "" {
					blobs = append(blobs, a.BlobID)
				}
			}
			return len(blobs) == 1, fmt.Sprint(blobs)
		})
		if _, err := s.pg.GetBlob(ctx, blobs[0]); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("raw media retained after processing: %v", err)
		}
	})
	t.Run("duplicate message id runs once", func(t *testing.T) {
		msg := domain.InboundMessage{ID: "dup-" + randID(4), CompanyID: c, Channel: domain.ChannelDev, Sender: "owner", Kind: domain.KindText, Text: "yardım", ReceivedAt: time.Now().UTC()}
		for range 3 {
			if err := worker.Accept(ctx, s.pg, s.q, msg); err != nil {
				t.Fatal(err)
			}
		}
		s.waitOutbox(c, "owner", "Yapabileceklerim")
		time.Sleep(300 * time.Millisecond)
		n := strings.Count(strings.Join(s.outbox(c, "owner"), "\n"), "Yapabileceklerim")
		if n != 1 {
			t.Fatalf("redelivered webhook handled %d times", n)
		}
	})
	t.Run("all inbound jobs completed", func(t *testing.T) {
		waitFor(t, 10*time.Second, "queue drain", func() (bool, string) {
			left := s.jobs(rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled)
			return len(left) == 0, ""
		})
		if bad := s.jobs(rivertype.JobStateDiscarded, rivertype.JobStateCancelled); len(bad) != 0 {
			t.Fatalf("failed jobs: %+v", bad[0])
		}
	})
}

func TestEventsAlertsAndWebhooks(t *testing.T) {
	type delivery struct {
		event string
		ok    bool
		body  string
	}
	var mu sync.Mutex
	var got []delivery
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, delivery{r.Header.Get("X-Ledgerbot-Event"), sign.Verify("whsec", r.Header.Get(sign.Header), b, time.Now(), time.Minute), string(b)})
		mu.Unlock()
	}))
	t.Cleanup(hook.Close)
	s := newStack(t, stackOpts{webhooks: []tenant.Webhook{{URL: hook.URL, Secret: "whsec", Events: []string{"draft.*", "stock.low"}}}})
	c := s.company("ev")

	r := s.call(c, http.MethodPost, "/v1/events", map[string]any{"type": "stock.low", "data": events.StockLowData{ProductID: "P002", Name: "Toner 85A", Unit: "adet", Stock: 3000, MinStock: 15000}})
	if r.Status != http.StatusAccepted {
		t.Fatalf("event: %d %s", r.Status, r.Raw)
	}
	s.waitOutbox(c, "owner", "⚠️ Kritik stok: Toner 85A, kalan 3 adet (en az 15).")
	s.waitOutbox(c, "accountant", "Kritik stok: Toner 85A")
	s.call(c, http.MethodPost, "/v1/events", map[string]any{"type": "stock.low", "data": events.StockLowData{ProductID: "P002", Name: "Toner 85A", Unit: "adet", Stock: 2000, MinStock: 15000}})
	s.async(c, "owner", "yardım")
	s.waitOutbox(c, "owner", "Yapabileceklerim")
	if n := strings.Count(strings.Join(s.outbox(c, "owner"), "\n"), "Kritik stok: Toner"); n != 1 {
		t.Fatalf("throttle failed: %d stock alerts", n)
	}

	s.async(c, "owner", "Ahmet Yılmaz'dan 1.000 TL tahsilat")
	waitFor(t, 15*time.Second, "signed webhooks", func() (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		seen := map[string]bool{}
		for _, d := range got {
			if !d.ok {
				return false, "bad signature on " + d.event
			}
			seen[d.event] = true
		}
		return seen["draft.created"] && seen["draft.committed"] && seen["stock.low"], ""
	})
	mu.Lock()
	for _, d := range got {
		if d.event == "message.handled" {
			t.Errorf("unsubscribed event delivered: %s", d.event)
		}
	}
	mu.Unlock()

	r = s.call(c, http.MethodPost, "/v1/simulator/run", nil)
	if r.Status != 200 || r.data()["events"].(float64) < 1 {
		t.Fatalf("simulator: %d %s", r.Status, r.Raw)
	}
	waitFor(t, 15*time.Second, "simulated events processed", func() (bool, string) {
		left := s.jobs(rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable)
		return len(left) == 0, ""
	})
}

func TestDataDeletion(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	c := s.company("ask-del")
	s.async(c, "accountant", "Ahmet Yılmaz'dan 900 TL tahsilat")
	s.waitOutbox(c, "accountant", "Onaylıyor musunuz")
	r := s.call(c, http.MethodPost, "/v1/deletion-requests", map[string]any{"channel": "dev", "subject": "accountant"})
	if r.Status != http.StatusAccepted {
		t.Fatalf("deletion: %d %s", r.Status, r.Raw)
	}
	id := r.data()["id"].(string)
	waitFor(t, 15*time.Second, "deletion completed", func() (bool, string) {
		g := s.call(c, http.MethodGet, "/v1/deletion-requests/"+id, nil)
		return g.data()["status"] == "completed", string(g.Raw)
	})
	g := s.call(c, http.MethodGet, "/v1/deletion-requests/"+id, nil)
	if g.data()["removed_rows"].(float64) < 1 {
		t.Fatalf("nothing removed: %s", g.Raw)
	}
	if _, err := s.pg.LatestPending(ctx, c, domain.ChannelDev, "accountant", time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending draft survived deletion: %v", err)
	}
	audit, _ := s.pg.ListAudit(ctx, c, 0, 500)
	for _, a := range audit {
		if strings.Contains(string(a.Detail), `"sender":"accountant"`) {
			t.Fatalf("audit still names the subject: %s", a.Detail)
		}
	}
}

func TestDeadLetters(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(fail.Close)
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	ev, _ := events.New(s.company("dl"), "draft.created", "ledgerbot", map[string]string{"x": "y"})
	res, err := s.river.Insert(ctx, queue.WebhookArgs{URL: fail.URL, Secret: "s", Event: ev}, &river.InsertOpts{MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGone) }))
	t.Cleanup(gone.Close)
	res2, err := s.river.Insert(ctx, queue.WebhookArgs{URL: gone.URL, Secret: "s", Event: ev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res3, err := s.river.Insert(ctx, queue.OutboundArgs{Out: domain.OutboundMessage{CompanyID: s.company("dl"), Channel: domain.ChannelTelegram, To: "1", Text: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "dead letter", func() (bool, string) {
		j, err := s.river.JobGet(ctx, res.Job.ID)
		if err != nil {
			return false, err.Error()
		}
		return j.State == rivertype.JobStateDiscarded, string(j.State)
	})
	j, _ := s.river.JobGet(ctx, res.Job.ID)
	if j.Attempt != 3 || len(j.Errors) != 3 || !strings.Contains(j.Errors[2].Error, "webhook status 500") {
		t.Fatalf("attempts %d errors %+v", j.Attempt, j.Errors)
	}
	for _, id := range []int64{res2.Job.ID, res3.Job.ID} {
		waitFor(t, 10*time.Second, "cancelled job", func() (bool, string) {
			j, err := s.river.JobGet(ctx, id)
			return err == nil && j.State == rivertype.JobStateCancelled, ""
		})
	}
	req, _ := http.NewRequest(http.MethodGet, s.srv.URL+"/admin/jobs?state=discarded", nil)
	req.Header.Set("Authorization", "Bearer it-admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"Kind":"webhook_delivery"`) || !strings.Contains(string(body), `"State":"discarded"`) || strings.Contains(string(body), `"State":"cancelled"`) {
		t.Fatalf("admin dead letters: %d %s", resp.StatusCode, body)
	}
}
