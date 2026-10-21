// Package feature holds black-box feature tests that drive ledgerbot through
// its HTTP API in-process (httptest, in-memory store, fake data and AI).
package feature

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/api"
	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/nlu"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/rag"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

const (
	demoKey  = "lb_demo_demosecret0000000000000000"
	demo2Key = "lb_demo2_featuresecret00000000000000"
	seedDir  = "../../seed/companies"
)

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

func (q *memQueue) all() []river.JobArgs {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]river.JobArgs(nil), q.jobs...)
}

func (q *memQueue) inbound() []queue.InboundArgs {
	var out []queue.InboundArgs
	for _, j := range q.all() {
		if a, ok := j.(queue.InboundArgs); ok {
			out = append(out, a)
		}
	}
	return out
}

// mutSource wraps the seed files and lets a test rewrite a company's contracts.
type mutSource struct {
	base   *tenant.FileSource
	mutate func(c domain.CompanyID, s *tenant.Settings, m *tenant.Matrix)
	down   atomic.Bool
}

func (m *mutSource) load(ctx context.Context, c domain.CompanyID) (tenant.Settings, tenant.Matrix, error) {
	if m.down.Load() {
		return tenant.Settings{}, tenant.Matrix{}, errBoom
	}
	s, err := m.base.Settings(ctx, c)
	if err != nil {
		return s, tenant.Matrix{}, err
	}
	mx, err := m.base.Matrix(ctx, c)
	if err != nil {
		return s, mx, err
	}
	if c == "demo2" {
		s.APIKeys = append(s.APIKeys, tenant.APIKey{ID: "feature", SHA256: digest(demo2Key), Scopes: []string{"*"}})
	}
	if m.mutate != nil {
		m.mutate(c, &s, &mx)
	}
	return s, mx, nil
}

func (m *mutSource) Settings(ctx context.Context, c domain.CompanyID) (tenant.Settings, error) {
	s, _, err := m.load(ctx, c)
	return s, err
}

func (m *mutSource) Matrix(ctx context.Context, c domain.CompanyID) (tenant.Matrix, error) {
	_, mx, err := m.load(ctx, c)
	return mx, err
}

func digest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type opts struct {
	mutate    func(c domain.CompanyID, s *tenant.Settings, m *tenant.Matrix)
	rps       float64
	burst     int
	knowledge bool
	ready     func(context.Context) error
	ttl       time.Duration
}

type env struct {
	t    *testing.T
	srv  *httptest.Server
	q    *memQueue
	st   *store.Mem
	dev  *channels.Dev
	data *fake.Provider
	idx  *rag.Mem
	src  *mutSource
}

func newEnv(t *testing.T, o opts) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ttl := o.ttl
	if ttl == 0 {
		ttl = time.Minute
	}
	src := &mutSource{base: &tenant.FileSource{Dir: seedDir}, mutate: o.mutate}
	tenants := tenant.NewCache(src, ttl, log)
	reg, err := ai.NewRegistry(ai.FakeConfig())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, q: &memQueue{}, st: store.NewMem(), dev: channels.NewDev(50, nil), data: fake.New(fake.NewMemLedger()), src: src}
	eng := &assistant.Engine{
		Tenants: tenants, Data: e.data, AI: reg, Store: e.st,
		FX: fx.NewConverter(fx.Static{"USD": 34, "EUR": 37.25}), Caps: assistant.NewRegistry(), Log: log,
	}
	if o.knowledge {
		e.idx = &rag.Mem{Embedder: ai.FakeEmbedder{Dims: 64}}
		eng.Knowledge = &assistant.Knowledge{Index: e.idx, MinScore: 0.2, Remember: func(ctx context.Context, c domain.CompanyID, source, ref, text string) error {
			return e.idx.Add(ctx, c, source, ref, text)
		}}
	}
	rps, burst := o.rps, o.burst
	if rps == 0 {
		rps, burst = 10000, 10000
	}
	s := &api.Server{
		Engine: eng, Tenants: tenants, Store: e.st, Queue: e.q, Log: log, Dev: e.dev,
		Channels:   channels.NewRegistry(e.dev, channels.Telegram{}, channels.Wuzapi{}, channels.WABA{}, channels.Email{}),
		DevEnabled: true, RateRPS: rps, RateBurst: burst, PublicURL: "https://lb.example", MetaAppSecret: "meta-secret",
		AdminToken: "admin-token", Ready: o.ready,
		Simulate: func(ctx context.Context, c domain.CompanyID) (int, error) {
			evs, err := worker.Simulate(ctx, e.data, c)
			if err != nil {
				return 0, err
			}
			args := make([]river.JobArgs, len(evs))
			for i, ev := range evs {
				args[i] = queue.EventArgs{Event: ev}
			}
			return len(evs), e.q.EnqueueMany(ctx, args)
		},
		Jobs: func(context.Context, string, int) (any, error) { return []any{}, nil },
	}
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	Status int
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

func (r resp) data() map[string]any {
	d, _ := r.Body["data"].(map[string]any)
	return d
}

func (r resp) errCode() string {
	e, _ := r.Body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (e *env) do(method, path, body string, hdr map[string]string) resp {
	e.t.Helper()
	return e.doKey(demoKey, method, path, body, hdr)
}

func (e *env) doKey(key, method, path, body string, hdr map[string]string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

type reply struct {
	Texts    []string       `json:"texts"`
	Language string         `json:"language"`
	Drafts   []domain.Draft `json:"drafts"`
	Intents  []nlu.Intent   `json:"intents"`
}

func (r reply) text() string { return strings.Join(r.Texts, "\n") }

func (r reply) draft(t *testing.T) domain.Draft {
	t.Helper()
	if len(r.Drafts) != 1 {
		t.Fatalf("expected exactly one draft, got %d: %q", len(r.Drafts), r.text())
	}
	return r.Drafts[0]
}

func decodeReply(t *testing.T, r resp) reply {
	t.Helper()
	if r.Status != http.StatusOK {
		t.Fatalf("status %d: %s", r.Status, r.Raw)
	}
	var env struct {
		Data reply `json:"data"`
	}
	if err := json.Unmarshal(r.Raw, &env); err != nil {
		t.Fatalf("decode reply: %v: %s", err, r.Raw)
	}
	return env.Data
}

func jsonBody(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// say sends a dev-channel text message for the demo company.
func (e *env) say(sender, text string) reply {
	e.t.Helper()
	return decodeReply(e.t, e.do(http.MethodPost, "/v1/dev/messages", jsonBody(map[string]any{"sender": sender, "text": text}), nil))
}

func (e *env) sayKey(key, sender, text string) reply {
	e.t.Helper()
	return decodeReply(e.t, e.doKey(key, http.MethodPost, "/v1/dev/messages", jsonBody(map[string]any{"sender": sender, "text": text}), nil))
}

// sendMedia sends a dev-channel media message.
func (e *env) sendMedia(sender, mime, filename string, data []byte) reply {
	e.t.Helper()
	media := map[string]any{"mime": mime, "data": base64.StdEncoding.EncodeToString(data)}
	if filename != "" {
		media["filename"] = filename
	}
	return decodeReply(e.t, e.do(http.MethodPost, "/v1/dev/messages", jsonBody(map[string]any{"sender": sender, "media": media}), nil))
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("expected %q in:\n%s", w, got)
		}
	}
}

func notContains(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(got, b) {
			t.Fatalf("did not expect %q in:\n%s", b, got)
		}
	}
}

func setSaveMode(mode domain.SaveMode) func(domain.CompanyID, *tenant.Settings, *tenant.Matrix) {
	return func(c domain.CompanyID, s *tenant.Settings, _ *tenant.Matrix) {
		if c == "demo" {
			s.SaveMode = mode
		}
	}
}

func (e *env) balance(customerID string) domain.Money {
	e.t.Helper()
	b, err := e.data.Balance(context.Background(), "demo", customerID)
	if err != nil {
		e.t.Fatal(err)
	}
	return b.Balance
}

func (e *env) customerID(name string) string {
	e.t.Helper()
	cs, err := e.data.FindCustomers(context.Background(), "demo", name, 2)
	if err != nil || len(cs) == 0 {
		e.t.Fatalf("customer %q: %v %v", name, cs, err)
	}
	return cs[0].ID
}

var errBoom = errors.New("database down")

func bytesEqualJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return bytes.Equal(ja, jb)
}
