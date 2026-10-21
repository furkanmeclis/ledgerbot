// Command ledgerbot runs the assistant layer.
//
//	ledgerbot migrate   apply database migrations (ledgerbot + River)
//	ledgerbot api       REST API and channel webhooks (stateless, scale out)
//	ledgerbot worker    background jobs and channel listeners (scale out)
//	ledgerbot all       api + worker in one process (development, demos)
//	ledgerbot healthcheck  exit 0 when the local HTTP server is healthy
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/api"
	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/config"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/apidriver"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/rag"
	"github.com/furkanmeclis/ledgerbot/internal/secretbox"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/telemetry"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
	"github.com/furkanmeclis/ledgerbot/migrations"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	mode := "all"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(mode); err != nil {
		fmt.Fprintln(os.Stderr, "ledgerbot:", err)
		os.Exit(1)
	}
}

func run(mode string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := telemetry.Setup(ctx, "ledgerbot-"+mode, version)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(c)
	}()

	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	pcfg.MaxConns = cfg.DBMaxConns
	db, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// The database may still be starting (compose, k8s): wait up to 60s.
	for i := 0; ; i++ {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := db.Ping(pctx)
		cancel()
		if err == nil {
			break
		}
		if i == 30 || ctx.Err() != nil {
			return fmt.Errorf("database unreachable: %w", err)
		}
		log.Info("waiting for database", "err", err)
		time.Sleep(2 * time.Second)
	}

	switch mode {
	case "migrate":
		return migrate(ctx, db, log)
	case "api", "worker", "all":
	default:
		return fmt.Errorf("unknown mode %q (migrate, api, worker, all)", mode)
	}
	if cfg.Dev && mode == "all" {
		if err := migrate(ctx, db, log); err != nil {
			return err
		}
	}

	app, err := build(cfg, db, log, mode != "api")
	if err != nil {
		return err
	}
	errc := make(chan error, 3)
	if mode != "api" {
		if err := app.river.Start(ctx); err != nil {
			return err
		}
		go app.listeners.Run(ctx)
		log.Info("worker started", "concurrency", cfg.WorkerConcurrency)
	}
	var srv *http.Server
	if mode != "worker" {
		srv = &http.Server{Addr: cfg.HTTPAddr, Handler: app.server.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
		go func() {
			log.Info("api listening", "addr", cfg.HTTPAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	} else {
		// Workers expose health and metrics too.
		srv = &http.Server{Addr: cfg.HTTPAddr, Handler: healthOnly(app.server), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		log.Error("fatal", "err", err)
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if mode != "api" {
		_ = app.river.Stop(shutdown)
	}
	app.store.Close()
	return nil
}

type app struct {
	store     *store.Batched
	server    *api.Server
	river     *river.Client[pgx.Tx]
	listeners *worker.Listeners
}

func build(cfg config.Config, db *pgxpool.Pool, log *slog.Logger, withWorkers bool) (*app, error) {
	var box *secretbox.Box
	if cfg.SecretKey != "" {
		b, err := secretbox.New(cfg.SecretKey)
		if err != nil {
			return nil, err
		}
		box = b
	} else {
		log.Warn("LEDGERBOT_SECRET_KEY is empty: channel credentials are stored unencrypted (dev mode)")
	}

	var src tenant.Source
	var dir tenant.Directory
	var simCompanies func() ([]domain.CompanyID, error)
	var lister tenant.Lister
	switch cfg.TenantSource {
	case "http":
		h := &tenant.HTTPSource{Base: cfg.HostAPIURL, Token: cfg.HostAPIToken}
		src, dir, lister = h, h, h
	default:
		f := &tenant.FileSource{Dir: cfg.SeedDir}
		src, dir, lister, simCompanies = f, f, f, f.Companies
	}
	tenants := tenant.NewCache(src, cfg.TenantTTL, log)

	var data dataprovider.Provider
	switch cfg.DataDriver {
	case "api":
		data = &apidriver.Driver{Base: cfg.HostAPIURL, Token: cfg.HostAPIToken}
	default:
		data = fake.New(fake.PGLedger{DB: db})
	}

	aiCfg, err := ai.LoadConfig(cfg.ProvidersFile)
	if err != nil {
		return nil, err
	}
	reg, err := ai.NewRegistry(aiCfg)
	if err != nil {
		return nil, err
	}
	var fxSrc fx.Source = fx.TCMB{}
	if cfg.FXSource == "static" {
		fxSrc = fx.Static{"USD": 34.5, "EUR": 37.25, "GBP": 43.9}
	}

	pg := &store.PG{DB: db}
	st := store.NewBatched(pg, 500, 100*time.Millisecond, log)
	workers := river.NewWorkers()
	rcfg := &river.Config{Logger: log.With("component", "river"), Workers: workers}
	if withWorkers {
		rcfg.Queues = queue.Queues(cfg.WorkerConcurrency)
		rcfg.PeriodicJobs = worker.PeriodicJobs(cfg.SimulateEvery)
	}
	rc, err := river.NewClient(riverpgxv5.New(db), rcfg)
	if err != nil {
		return nil, err
	}
	q := queue.River{C: rc}

	dev := channels.NewDev(50, pg)
	chans := channels.NewRegistry(dev, channels.Telegram{}, channels.Wuzapi{}, channels.WABA{}, channels.Email{})
	var index rag.Index
	if emb, err := reg.Embedder(""); err == nil {
		index = &rag.PG{DB: db, Embedder: emb}
	} else {
		log.Warn("RAG disabled: no embedding provider", "err", err)
	}
	engine := &assistant.Engine{
		Tenants: tenants, Data: data, AI: reg, Store: st, Events: queue.Publisher{Q: q},
		FX: fx.NewConverter(fxSrc), Caps: assistant.NewRegistry(), Log: log,
	}
	if index != nil {
		engine.Knowledge = &assistant.Knowledge{
			Index: index, MinScore: 0.25,
			Remember: func(ctx context.Context, c domain.CompanyID, source, ref, text string) error {
				_, err := q.Enqueue(ctx, queue.RAGIndexArgs{CompanyID: c, Source: source, Ref: ref, Text: text})
				return err
			},
		}
	}
	deps := &worker.Deps{
		Engine: engine, Channels: chans, Dev: dev, Store: st, Box: box, Tenants: tenants, Directory: dir,
		Queue: q, Data: data, Log: log, PerCompanyInflight: cfg.PerCompanyInflight, SimCompanies: simCompanies, Knowledge: index, Companies: lister,
	}
	// Workers must be registered even in api mode so inserts validate job kinds.
	worker.Register(workers, deps)

	host, _ := os.Hostname()
	srv := &api.Server{
		Engine: engine, Tenants: tenants, Store: st, Queue: q, Channels: chans, Dev: dev, Box: box, Log: log,
		Ready: db.Ping, AdminToken: cfg.AdminToken, MetaAppSecret: cfg.MetaAppSecret, PublicURL: cfg.PublicURL,
		DevEnabled: cfg.Dev, RateRPS: cfg.RateRPS, RateBurst: cfg.RateBurst,
		Jobs: func(ctx context.Context, state string, limit int) (any, error) {
			params := river.NewJobListParams().States(rivertype.JobState(state)).First(max(min(limit, 200), 1))
			res, err := rc.JobList(ctx, params)
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
			return len(evs), q.EnqueueMany(ctx, args)
		},
	}
	listeners := &worker.Listeners{
		DB: db, Store: st, Box: box, Channels: chans, Queue: q, Log: log,
		Owner: host + "-" + events.NewID("w")[2:10], Max: cfg.MaxListeners,
	}
	return &app{store: st, server: srv, river: rc, listeners: listeners}, nil
}

func migrate(ctx context.Context, db *pgxpool.Pool, log *slog.Logger) error {
	m, err := rivermigrate.New(riverpgxv5.New(db), nil)
	if err != nil {
		return err
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	v, _ := goose.GetDBVersionContext(ctx, sqlDB)
	log.Info("migrations applied", "version", v)
	return nil
}

// healthcheck is used by container HEALTHCHECK (the image has no shell or curl).
func healthcheck() int {
	addr := os.Getenv("LEDGERBOT_HTTP_ADDR")
	if addr == "" || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
		if addr == "127.0.0.1" {
			addr += ":8080"
		}
	}
	cl := &http.Client{Timeout: 2 * time.Second}
	resp, err := cl.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func healthOnly(s *api.Server) http.Handler {
	full := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			full.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
