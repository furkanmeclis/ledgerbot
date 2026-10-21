package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

const demoKey = "lb_demo_demosecret0000000000000000"

type memQueue struct {
	mu   sync.Mutex
	jobs []river.JobArgs
}

func (q *memQueue) Enqueue(_ context.Context, a river.JobArgs) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, a)
	return int64(len(q.jobs)), nil
}

func (q *memQueue) EnqueueMany(ctx context.Context, as []river.JobArgs) error {
	for _, a := range as {
		_, _ = q.Enqueue(ctx, a)
	}
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, *memQueue, *store.Mem) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenants := tenant.NewCache(&tenant.FileSource{Dir: "../../seed/companies"}, time.Minute, log)
	reg, _ := ai.NewRegistry(ai.FakeConfig())
	st := store.NewMem()
	q := &memQueue{}
	eng := &assistant.Engine{Tenants: tenants, Data: fake.New(fake.NewMemLedger()), AI: reg, Store: st, FX: fx.NewConverter(fx.Static{"USD": 34}), Caps: assistant.NewRegistry(), Log: log}
	dev := channels.NewDev(20, nil)
	s := &Server{
		Engine: eng, Tenants: tenants, Store: st, Queue: q, Log: log, Dev: dev,
		Channels:   channels.NewRegistry(dev, channels.Telegram{}, channels.Wuzapi{}, channels.WABA{}, channels.Email{}),
		DevEnabled: true, RateRPS: 1000, RateBurst: 1000, PublicURL: "https://lb.example", MetaAppSecret: "meta",
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, q, st
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+demoKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer lb_demo_wrong")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	if code, body := call(t, srv, http.MethodGet, "/v1/capabilities", "", nil); code != 200 || body["data"] == nil {
		t.Fatalf("capabilities: %d %v", code, body)
	}
}

func TestAnalyzeAndApprove(t *testing.T) {
	srv, _, _ := newTestServer(t)
	code, body := call(t, srv, http.MethodPost, "/v1/messages", `{"actor_id":"u-acc","text":"Ahmet Yılmaz'dan 1.000 TL tahsilat"}`, nil)
	if code != 200 {
		t.Fatalf("analyze: %d %v", code, body)
	}
	drafts := body["data"].(map[string]any)["drafts"].([]any)
	id := drafts[0].(map[string]any)["id"].(string)
	if code, body = call(t, srv, http.MethodPost, "/v1/drafts/"+id+"/approve", `{"actor_id":"u-acc"}`, nil); code != 200 {
		t.Fatalf("approve: %d %v", code, body)
	}
	if code, _ = call(t, srv, http.MethodPost, "/v1/drafts/"+id+"/approve", `{"actor_id":"u-acc"}`, nil); code != http.StatusConflict {
		t.Fatalf("double approve: %d", code)
	}
	if code, _ = call(t, srv, http.MethodPost, "/v1/messages", `{"actor_id":"nobody","text":"x"}`, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown actor: %d", code)
	}
}

func TestIdempotency(t *testing.T) {
	srv, q, _ := newTestServer(t)
	h := map[string]string{"Idempotency-Key": "k1"}
	body := `{"channel":"dev","to":"owner","text":"merhaba"}`
	c1, b1 := call(t, srv, http.MethodPost, "/v1/messages/send", body, h)
	c2, b2 := call(t, srv, http.MethodPost, "/v1/messages/send", body, h)
	if c1 != 202 || c2 != 202 || len(q.jobs) != 1 {
		t.Fatalf("idempotent replay failed: %d %d jobs=%d", c1, c2, len(q.jobs))
	}
	j1, _ := json.Marshal(b1)
	j2, _ := json.Marshal(b2)
	if !bytes.Equal(j1, j2) {
		t.Fatalf("replayed body differs: %s vs %s", j1, j2)
	}
	if c, _ := call(t, srv, http.MethodPost, "/v1/messages/send", `{"channel":"dev","to":"owner","text":"farklı"}`, h); c != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with another body: %d", c)
	}
}

func TestChannelsAndWebhook(t *testing.T) {
	srv, q, _ := newTestServer(t)
	code, body := call(t, srv, http.MethodPost, "/v1/channels", `{"kind":"telegram","name":"bot","config":{"bot_token":"T","mode":"webhook","secret_token":"s"}}`, nil)
	if code != 200 {
		t.Fatalf("create channel: %d %v", code, body)
	}
	ch := body["data"].(map[string]any)["channel"].(map[string]any)
	hook := body["data"].(map[string]any)["webhook_url"].(string)
	if !strings.HasPrefix(hook, "https://lb.example/webhooks/telegram/") {
		t.Fatalf("webhook url %s", hook)
	}
	upd := `{"update_id":1,"message":{"message_id":1,"date":1760000000,"chat":{"id":100000002,"type":"private"},"text":"yardım"}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/telegram/"+ch["id"].(string), strings.NewReader(upd))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "s")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 || len(q.jobs) != 1 {
		t.Fatalf("webhook: %d jobs=%d", resp.StatusCode, len(q.jobs))
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/webhooks/telegram/"+ch["id"].(string), strings.NewReader(upd))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned webhook accepted: %d", resp.StatusCode)
	}
}

func TestMetaDeletionCallback(t *testing.T) {
	srv, q, _ := newTestServer(t)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":"123","algorithm":"HMAC-SHA256"}`))
	mac := hmac.New(sha256.New, []byte("meta"))
	mac.Write([]byte(payload))
	signed := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "." + payload
	resp, err := http.PostForm(srv.URL+"/webhooks/meta/data-deletion", map[string][]string{"signed_request": {signed}})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out["confirmation_code"] == "" || !strings.Contains(out["url"], out["confirmation_code"]) || len(q.jobs) != 1 {
		t.Fatalf("meta callback: %d %v jobs=%d", resp.StatusCode, out, len(q.jobs))
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"a":1}`)
	now := time.Unix(1_760_000_000, 0)
	h := worker.Sign("sec", now.Unix(), body)
	if !VerifySignature("sec", h, body, now, time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature("sec", h, body, now.Add(10*time.Minute), time.Minute) || VerifySignature("other", h, body, now, time.Minute) {
		t.Fatal("expired or wrong-key signature accepted")
	}
}

func TestParseKey(t *testing.T) {
	if c, ok := ParseKey("lb_acme_corp_abc123"); !ok || c != "acme_corp" {
		t.Fatalf("got %q %v", c, ok)
	}
	for _, bad := range []string{"", "lb_", "lb_x_", "xx_demo_abc"} {
		if _, ok := ParseKey(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSettingsRedacted(t *testing.T) {
	srv, _, _ := newTestServer(t)
	code, body := call(t, srv, http.MethodGet, "/v1/settings", "", nil)
	if code != 200 {
		t.Fatalf("settings: %d %v", code, body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "59c7b9a3c6f7d1838ff3cb12d947dd5b75a2705ad9374149e2d918bb37eda2ed") || !strings.Contains(string(raw), "permissions") {
		t.Fatalf("api key hash must be redacted: %s", raw)
	}
}
