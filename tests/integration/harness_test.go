// Package integration exercises ledgerbot against a real PostgreSQL (with
// pgvector) and a real River queue. Tests skip unless
// LEDGERBOT_TEST_DATABASE_URL is set.
package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/api"
	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/rag"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func keyFor(c domain.CompanyID) string { return "lb_" + string(c) + "_itsecret00000000000000" }

// source fabricates companies whose id starts with the run prefix.
type source struct {
	prefix   string
	webhooks []tenant.Webhook
}

func (s source) Settings(_ context.Context, c domain.CompanyID) (tenant.Settings, error) {
	if !strings.HasPrefix(string(c), s.prefix) {
		return tenant.Settings{}, tenant.ErrUnknownCompany
	}
	mode := domain.SaveAuto
	if strings.Contains(string(c), "ask") {
		mode = domain.SaveAskApproval
	}
	return tenant.Settings{
		CompanyID: c, CompanyName: "IT " + string(c), Version: 1, SaveMode: mode, Locale: "tr-TR",
		APIKeys:  []tenant.APIKey{{ID: "it", SHA256: digest(keyFor(c)), Scopes: []string{"*"}}},
		Webhooks: s.webhooks,
		Rules: []tenant.Rule{
			{Event: "stock.low", NotifyRoles: []string{"owner", "accountant"}, Throttle: "6h"},
			{Event: "invoice.overdue", NotifyRoles: []string{"owner"}, Throttle: "24h"},
		},
	}, nil
}

func (s source) Matrix(_ context.Context, c domain.CompanyID) (tenant.Matrix, error) {
	if !strings.HasPrefix(string(c), s.prefix) {
		return tenant.Matrix{}, tenant.ErrUnknownCompany
	}
	return tenant.Matrix{
		CompanyID: c, Version: 1,
		Roles: map[string]tenant.Role{
			"owner":      {Capabilities: []string{"*"}},
			"accountant": {Capabilities: []string{"*"}, SaveMode: domain.SaveAskApproval},
		},
		Users: []tenant.User{
			{ID: "u-owner", Name: "Sahip", Role: "owner", Addresses: map[domain.ChannelKind]string{domain.ChannelDev: "owner"}},
			{ID: "u-acc", Name: "Muhasebe", Role: "accountant", Addresses: map[domain.ChannelKind]string{domain.ChannelDev: "accountant"}},
		},
	}, nil
}

type fastRetry struct{}

func (fastRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now().Add(10 * time.Millisecond) }

type stackOpts struct {
	webhooks    []tenant.Webhook
	inflight    int
	concurrency int
	wrapData    func(dataprovider.Provider) dataprovider.Provider
}

type stack struct {
	t      *testing.T
	db     *pgxpool.Pool
	schema string
	prefix string
	pg     *store.PG
	data   *fake.Provider
	river  *river.Client[pgx.Tx]
	q      queue.Enqueuer
	dev    *channels.Dev
	index  *rag.PG
	srv    *httptest.Server
	engine *assistant.Engine
}

// migrateRiver installs River's tables into a private schema so concurrent
// test packages never steal each other's jobs.
func migrateRiver(t *testing.T, db *pgxpool.Pool, schema string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	m, err := rivermigrate.New(riverpgxv5.New(db), &rivermigrate.Config{Schema: schema, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatal(err)
	}
}

func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	db := testdb.Open(t)
	ctx := context.Background()
	s := &stack{t: t, db: db, schema: "lbit_" + randID(6), prefix: "it" + randID(4), pg: &store.PG{DB: db}}
	migrateRiver(t, db, s.schema)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenants := tenant.NewCache(source{prefix: s.prefix, webhooks: o.webhooks}, time.Hour, log)
	reg, err := ai.NewRegistry(ai.FakeConfig())
	if err != nil {
		t.Fatal(err)
	}
	s.data = fake.New(fake.PGLedger{DB: db})
	var data dataprovider.Provider = s.data
	if o.wrapData != nil {
		data = o.wrapData(data)
	}
	s.dev = channels.NewDev(200, s.pg)
	chans := channels.NewRegistry(s.dev, channels.Telegram{}, channels.Wuzapi{}, channels.WABA{}, channels.Email{})
	workers := river.NewWorkers()
	conc := o.concurrency
	if conc == 0 {
		conc = 8
	}
	rc, err := river.NewClient(riverpgxv5.New(db), &river.Config{
		Schema: s.schema, Logger: log, Workers: workers, Queues: queue.Queues(conc),
		FetchCooldown: 5 * time.Millisecond, FetchPollInterval: 50 * time.Millisecond, RetryPolicy: fastRetry{},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.river = rc
	s.q = queue.River{C: rc}
	emb, err := reg.Embedder("")
	if err != nil {
		t.Fatal(err)
	}
	s.index = &rag.PG{DB: db, Embedder: emb}
	s.engine = &assistant.Engine{
		Tenants: tenants, Data: data, AI: reg, Store: s.pg, Events: queue.Publisher{Q: s.q},
		FX: fx.NewConverter(fx.Static{"USD": 34}), Caps: assistant.NewRegistry(), Log: log,
		Knowledge: &assistant.Knowledge{Index: s.index, MinScore: 0.25, Remember: func(ctx context.Context, c domain.CompanyID, source, ref, text string) error {
			_, err := s.q.Enqueue(ctx, queue.RAGIndexArgs{CompanyID: c, Source: source, Ref: ref, Text: text})
			return err
		}},
	}
	worker.Register(workers, &worker.Deps{
		Engine: s.engine, Channels: chans, Dev: s.dev, Store: s.pg, Tenants: tenants, Queue: s.q, Data: data, Log: log,
		PerCompanyInflight: o.inflight, Knowledge: s.index,
	})
	srv := &api.Server{
		Engine: s.engine, Tenants: tenants, Store: s.pg, Queue: s.q, Channels: chans, Dev: s.dev, Log: log,
		Ready: db.Ping, AdminToken: "it-admin", MetaAppSecret: "it-meta", PublicURL: "https://it.example",
		DevEnabled: true, RateRPS: 1e6, RateBurst: 1e6,
		Jobs: func(ctx context.Context, state string, limit int) (any, error) {
			res, err := rc.JobList(ctx, river.NewJobListParams().States(rivertype.JobState(state)).First(max(min(limit, 200), 1)))
			if err != nil {
				return nil, err
			}
			return res.Jobs, nil
		},
		Simulate: func(ctx context.Context, c domain.CompanyID) (int, error) {
			evs, err := worker.Simulate(ctx, data, c)
			if err != nil {
				return 0, err
			}
			args := make([]river.JobArgs, len(evs))
			for i, e := range evs {
				args[i] = queue.EventArgs{Event: e}
			}
			return len(evs), s.q.EnqueueMany(ctx, args)
		},
	}
	s.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(s.srv.Close)
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rc.Stop(c)
	})
	t.Cleanup(func() { s.purge() })
	return s
}

// purge removes rows this run wrote to shared tables.
func (s *stack) purge() {
	ctx := context.Background()
	like := s.prefix + "%"
	for _, q := range []string{
		`DELETE FROM drafts WHERE company_id LIKE $1`, `DELETE FROM fingerprints WHERE company_id LIKE $1`,
		`DELETE FROM ai_usage WHERE company_id LIKE $1`, `DELETE FROM audit_log WHERE company_id LIKE $1`,
		`DELETE FROM channel_connections WHERE company_id LIKE $1`, `DELETE FROM idempotency_keys WHERE company_id LIKE $1`,
		`DELETE FROM throttles WHERE company_id LIKE $1`, `DELETE FROM dev_outbox WHERE company_id LIKE $1`,
		`DELETE FROM fake_ledger WHERE company_id LIKE $1`, `DELETE FROM rag_chunks WHERE company_id LIKE $1`,
		`DELETE FROM deletion_requests WHERE company_id LIKE $1`,
	} {
		_, _ = s.db.Exec(ctx, q, like)
	}
}

func (s *stack) company(name string) domain.CompanyID { return domain.CompanyID(s.prefix + "-" + name) }

type resp struct {
	Status int
	Raw    []byte
	Body   map[string]any
}

func (r resp) data() map[string]any {
	d, _ := r.Body["data"].(map[string]any)
	return d
}

func (s *stack) call(c domain.CompanyID, method, path string, body any) resp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, s.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+keyFor(c))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

// async enqueues a dev message through the API and returns its message id.
func (s *stack) async(c domain.CompanyID, sender, text string) string {
	s.t.Helper()
	r := s.call(c, http.MethodPost, "/v1/dev/messages", map[string]any{"sender": sender, "text": text, "async": true})
	if r.Status != http.StatusAccepted {
		s.t.Fatalf("async: %d %s", r.Status, r.Raw)
	}
	return r.data()["message_id"].(string)
}

// waitFor polls cond until it returns true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(d)
	last := ""
	for time.Now().Before(deadline) {
		ok, info := cond()
		if ok {
			return
		}
		last = info
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last: %s", what, last)
}

// outbox returns the dev replies a company sent to a sender.
func (s *stack) outbox(c domain.CompanyID, to string) []string {
	s.t.Helper()
	msgs, err := s.dev.Outbox(context.Background(), c, to)
	if err != nil {
		s.t.Fatal(err)
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return out
}

func (s *stack) waitOutbox(c domain.CompanyID, to, want string) []string {
	s.t.Helper()
	var got []string
	waitFor(s.t, 15*time.Second, fmt.Sprintf("%q in %s/%s outbox", want, c, to), func() (bool, string) {
		got = s.outbox(c, to)
		return strings.Contains(strings.Join(got, "\n"), want), strings.Join(got, " | ")
	})
	return got
}

func (s *stack) jobs(states ...rivertype.JobState) []*rivertype.JobRow {
	s.t.Helper()
	res, err := s.river.JobList(context.Background(), river.NewJobListParams().States(states...).First(1000))
	if err != nil {
		s.t.Fatal(err)
	}
	return res.Jobs
}
