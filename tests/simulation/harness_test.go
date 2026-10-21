// Package simulation runs in-process load and behaviour simulations of
// ledgerbot (many companies, a business day, approval storms, bursts) and
// emits one machine-readable "SIMRESULT {json}" line per simulation.
package simulation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/api"
	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/nlu"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func scale() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("LEDGERBOT_SIM_SCALE"), 64); err == nil && v > 0 {
		return v
	}
	return 1
}

func scaled(n int) int { return max(int(math.Round(float64(n)*scale())), 1) }

func keyFor(c domain.CompanyID) string { return "lb_" + string(c) + "_simsecret0000000000000000" }

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// genSource fabricates companies on demand: any id with a known prefix exists.
type genSource struct {
	prefixes map[string]domain.SaveMode
}

func (g genSource) mode(c domain.CompanyID) (domain.SaveMode, bool) {
	for p, m := range g.prefixes {
		if strings.HasPrefix(string(c), p) {
			return m, true
		}
	}
	return "", false
}

func (g genSource) Settings(_ context.Context, c domain.CompanyID) (tenant.Settings, error) {
	mode, ok := g.mode(c)
	if !ok {
		return tenant.Settings{}, tenant.ErrUnknownCompany
	}
	return tenant.Settings{
		CompanyID: c, CompanyName: "Firma " + string(c), Version: 1, SaveMode: mode, Locale: "tr-TR",
		APIKeys: []tenant.APIKey{{ID: "sim", SHA256: digest(keyFor(c)), Scopes: []string{"*"}}},
		Rules: []tenant.Rule{
			{Event: "stock.low", NotifyRoles: []string{"owner", "accountant"}, Throttle: "6h"},
			{Event: "invoice.overdue", NotifyRoles: []string{"owner"}, Throttle: "24h"},
		},
	}, nil
}

func (g genSource) Matrix(_ context.Context, c domain.CompanyID) (tenant.Matrix, error) {
	if _, ok := g.mode(c); !ok {
		return tenant.Matrix{}, tenant.ErrUnknownCompany
	}
	return tenant.Matrix{
		CompanyID: c, Version: 1,
		Roles: map[string]tenant.Role{
			"owner":      {Capabilities: []string{"*"}},
			"accountant": {Capabilities: []string{"*"}, SaveMode: domain.SaveAskApproval},
			"viewer":     {Capabilities: []string{"query_*", "daily_summary", "help"}, SaveMode: domain.SaveReplyOnly},
		},
		Users: []tenant.User{
			{ID: "u-owner", Name: "Sahip", Role: "owner", Addresses: map[domain.ChannelKind]string{domain.ChannelDev: "owner"}},
			{ID: "u-acc", Name: "Muhasebe", Role: "accountant", Addresses: map[domain.ChannelKind]string{domain.ChannelDev: "accountant"}},
			{ID: "u-view", Name: "İzleyici", Role: "viewer", Addresses: map[domain.ChannelKind]string{domain.ChannelDev: "viewer"}},
		},
	}, nil
}

type nopQueue struct{}

func (nopQueue) Enqueue(context.Context, river.JobArgs) (int64, error) { return 1, nil }
func (nopQueue) EnqueueMany(context.Context, []river.JobArgs) error    { return nil }

// slowProvider delays commits like a remote host API would.
type slowProvider struct {
	dataprovider.Provider
	delay time.Duration
}

func (s slowProvider) Commit(ctx context.Context, c domain.CompanyID, r dataprovider.Record) (string, error) {
	time.Sleep(s.delay)
	return s.Provider.Commit(ctx, c, r)
}

type simEnv struct {
	srv     *httptest.Server
	client  *http.Client
	engine  *assistant.Engine
	data    *fake.Provider
	st      *store.Mem
	dev     *channels.Dev
	tenants *tenant.Cache
}

type simOpts struct {
	prefixes  map[string]domain.SaveMode
	rps       float64
	burst     int
	conns     int
	wrapData  func(dataprovider.Provider) dataprovider.Provider
	devLimit  int
	syncLimit time.Duration
}

func newSimEnv(t testing.TB, o simOpts) *simEnv {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenants := tenant.NewCache(genSource{prefixes: o.prefixes}, time.Hour, log)
	reg, err := ai.NewRegistry(ai.FakeConfig())
	if err != nil {
		t.Fatal(err)
	}
	e := &simEnv{data: fake.New(fake.NewMemLedger()), st: store.NewMem(), tenants: tenants}
	var data dataprovider.Provider = e.data
	if o.wrapData != nil {
		data = o.wrapData(data)
	}
	if o.devLimit == 0 {
		o.devLimit = 100
	}
	e.dev = channels.NewDev(o.devLimit, nil)
	e.engine = &assistant.Engine{
		Tenants: tenants, Data: data, AI: reg, Store: e.st,
		FX: fx.NewConverter(fx.Static{"USD": 34, "EUR": 37}), Caps: assistant.NewRegistry(), Log: log,
	}
	rps, burst := o.rps, o.burst
	if rps == 0 {
		rps, burst = 1e9, 1<<30
	}
	s := &api.Server{
		Engine: e.engine, Tenants: tenants, Store: e.st, Queue: nopQueue{}, Log: log, Dev: e.dev,
		Channels:   channels.NewRegistry(e.dev, channels.Telegram{}, channels.Wuzapi{}, channels.WABA{}, channels.Email{}),
		DevEnabled: true, RateRPS: rps, RateBurst: burst, PublicURL: "https://sim.example", SyncTimeout: o.syncLimit,
	}
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	conns := max(o.conns, 16)
	tr := &http.Transport{MaxIdleConns: conns * 2, MaxIdleConnsPerHost: conns * 2, MaxConnsPerHost: conns * 2, IdleConnTimeout: 30 * time.Second}
	e.client = &http.Client{Transport: tr, Timeout: 60 * time.Second}
	t.Cleanup(tr.CloseIdleConnections)
	return e
}

type reply struct {
	Texts   []string       `json:"texts"`
	Drafts  []domain.Draft `json:"drafts"`
	Intents []nlu.Intent   `json:"intents"`
}

func (r reply) text() string { return strings.Join(r.Texts, "\n") }

// call performs one API request and returns status, body and latency.
func (e *simEnv) call(c domain.CompanyID, method, path string, body any) (int, []byte, time.Duration, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+keyFor(c))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	res, err := e.client.Do(req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	return res.StatusCode, raw, time.Since(start), err
}

// say sends a dev message and decodes the reply.
func (e *simEnv) say(c domain.CompanyID, sender, text string) (reply, time.Duration, error) {
	code, raw, d, err := e.call(c, http.MethodPost, "/v1/dev/messages", map[string]string{"sender": sender, "text": text})
	if err != nil {
		return reply{}, d, err
	}
	if code != http.StatusOK {
		return reply{}, d, fmt.Errorf("status %d: %s", code, raw)
	}
	var env struct {
		Data reply `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return reply{}, d, err
	}
	return env.Data, d, nil
}

// stats collects latencies and errors from many goroutines.
type stats struct {
	mu     sync.Mutex
	lat    []time.Duration
	errs   int
	sample []string
}

func (s *stats) ok(d time.Duration) {
	s.mu.Lock()
	s.lat = append(s.lat, d)
	s.mu.Unlock()
}

func (s *stats) fail(d time.Duration, format string, args ...any) {
	s.mu.Lock()
	s.lat = append(s.lat, d)
	s.errs++
	if len(s.sample) < 10 {
		s.sample = append(s.sample, fmt.Sprintf(format, args...))
	}
	s.mu.Unlock()
}

func (s *stats) errors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errs
}

func percentile(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	i = min(max(i, 0), len(sorted)-1)
	return float64(sorted[i].Microseconds()) / 1000
}

// report logs the SIMRESULT line and returns the summary map.
func (s *stats) report(t testing.TB, name string, elapsed time.Duration, extra map[string]any) map[string]any {
	t.Helper()
	s.mu.Lock()
	lat := slices.Clone(s.lat)
	errs := s.errs
	sample := slices.Clone(s.sample)
	s.mu.Unlock()
	slices.Sort(lat)
	ms := float64(elapsed.Microseconds()) / 1000
	res := map[string]any{
		"name": name, "ops": len(lat), "errors": errs, "duration_ms": round(ms),
		"p50_ms": round(percentile(lat, 50)), "p95_ms": round(percentile(lat, 95)), "p99_ms": round(percentile(lat, 99)),
		"throughput_ops_s": round(float64(len(lat)) / math.Max(elapsed.Seconds(), 1e-9)), "scale": scale(),
	}
	if len(lat) > 0 {
		res["max_ms"] = round(float64(lat[len(lat)-1].Microseconds()) / 1000)
	}
	for k, v := range extra {
		res[k] = v
	}
	b, _ := json.Marshal(res)
	t.Logf("SIMRESULT %s", b)
	for _, m := range sample {
		t.Logf("sample error: %s", m)
	}
	return res
}

func round(f float64) float64 { return math.Round(f*1000) / 1000 }

// pool runs fn(i) for i in [0,n) on w goroutines.
func pool(n, w int, fn func(i int)) {
	var wg sync.WaitGroup
	ch := make(chan int)
	for range max(w, 1) {
		wg.Go(func() {
			for i := range ch {
				fn(i)
			}
		})
	}
	for i := range n {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

func plain(m domain.Money) string { return strings.TrimSuffix(m.String(), " TL") }
