// Package api is ledgerbot's HTTP surface: the versioned REST API for host
// developers (/v1), channel webhooks (/webhooks), the Meta data-deletion
// callback, health checks and Prometheus metrics. There is no UI.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/metrics"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/secretbox"
	"github.com/furkanmeclis/ledgerbot/internal/sign"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/telemetry"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Server holds the API dependencies.
type Server struct {
	Engine   *assistant.Engine
	Tenants  *tenant.Cache
	Store    store.Store
	Queue    queue.Enqueuer
	Channels *channels.Registry
	Dev      *channels.Dev
	Box      *secretbox.Box
	Log      *slog.Logger
	Ready    func(context.Context) error
	// Jobs lists jobs by state for the admin API (nil disables it).
	Jobs func(ctx context.Context, state string, limit int) (any, error)
	// Simulate runs one simulator round synchronously (dev only).
	Simulate func(ctx context.Context, c domain.CompanyID) (int, error)

	AdminToken    string
	MetaAppSecret string
	PublicURL     string
	DevEnabled    bool
	RateRPS       float64
	RateBurst     int
	SyncTimeout   time.Duration

	limMu    sync.Mutex
	limiters map[domain.CompanyID]*rate.Limiter
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	if s.SyncTimeout <= 0 {
		s.SyncTimeout = 25 * time.Second
	}
	s.limiters = map[domain.CompanyID]*rate.Limiter{}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, s.observe)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", s.readyz)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/webhooks", func(r chi.Router) {
		r.Post("/meta/data-deletion", s.metaDeletion)
		r.Get("/{kind}/{connID}", s.channelWebhook) // Meta verification challenge
		r.Post("/{kind}/{connID}", s.channelWebhook)
	})
	r.Get("/deletion-status/{code}", s.deletionStatus)

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.auth, s.rateLimit, s.idempotency)
		r.Get("/capabilities", s.capabilities)
		r.With(scope("messages")).Post("/messages", s.analyze)
		r.With(scope("messages")).Post("/messages/send", s.send)
		r.With(scope("drafts")).Get("/drafts", s.listDrafts)
		r.With(scope("drafts")).Get("/drafts/{id}", s.getDraft)
		r.With(scope("drafts")).Post("/drafts/{id}/approve", s.decideDraft(true))
		r.With(scope("drafts")).Post("/drafts/{id}/reject", s.decideDraft(false))
		r.With(scope("channels")).Get("/channels", s.listChannels)
		r.With(scope("channels")).Post("/channels", s.upsertChannel)
		r.With(scope("channels")).Delete("/channels/{id}", s.deleteChannel)
		r.With(scope("channels")).Get("/channels/{id}/status", s.channelStatus)
		r.With(scope("channels")).Post("/channels/{id}/connect", s.channelAction("connect"))
		r.With(scope("channels")).Get("/channels/{id}/qr", s.channelAction("qr"))
		r.With(scope("channels")).Post("/channels/{id}/logout", s.channelAction("logout"))
		r.With(scope("events")).Post("/events", s.ingestEvent)
		r.With(scope("usage")).Get("/usage", s.usage)
		r.With(scope("audit")).Get("/audit", s.audit)
		r.With(scope("deletion")).Post("/deletion-requests", s.createDeletion)
		r.With(scope("deletion")).Get("/deletion-requests/{id}", s.getDeletion)
		if s.DevEnabled {
			r.With(scope("dev")).Post("/dev/messages", s.devMessage)
			r.With(scope("dev")).Get("/dev/outbox", s.devOutbox)
			r.With(scope("dev")).Post("/simulator/run", s.simulate)
		}
	})

	r.Route("/admin", func(r chi.Router) {
		r.Use(s.adminAuth)
		r.Get("/jobs", s.adminJobs)
		r.Post("/companies/{id}/invalidate", s.adminInvalidate)
	})
	return r
}

// --- envelope -------------------------------------------------------------------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": apiError{Code: code, Message: msg}})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, channels.MaxBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// --- middleware -------------------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
	buf    *bytes.Buffer // captures body for idempotency
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.buf != nil {
		w.buf.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := telemetry.Tracer().Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", sw.status))
		metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(sw.status/100)+"xx").Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type ctxKey int

const (
	ctxSnap ctxKey = iota
	ctxKeyScopes
)

func snapFrom(r *http.Request) *tenant.Snapshot { return r.Context().Value(ctxSnap).(*tenant.Snapshot) }

// ParseKey extracts the company id from "lb_<company>_<secret>".
func ParseKey(key string) (domain.CompanyID, bool) {
	if !strings.HasPrefix(key, "lb_") {
		return "", false
	}
	rest := key[3:]
	i := strings.LastIndexByte(rest, '_')
	if i <= 0 || i == len(rest)-1 {
		return "", false
	}
	return domain.CompanyID(rest[:i]), true
}

// auth authenticates "Authorization: Bearer lb_<company>_<secret>" against the
// hashed keys in the company's settings, and verifies the optional HMAC.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		company, valid := ParseKey(key)
		if !valid {
			fail(w, http.StatusUnauthorized, "unauthorized", "missing or malformed API key")
			return
		}
		snap, err := s.Tenants.Get(r.Context(), company)
		if errors.Is(err, tenant.ErrUnknownCompany) {
			fail(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
			return
		}
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "settings_unavailable", "company settings are unreachable")
			return
		}
		sum := sha256.Sum256([]byte(key))
		digest := hex.EncodeToString(sum[:])
		var scopes []string
		found := false
		for _, k := range snap.Settings.APIKeys {
			if subtle.ConstantTimeCompare([]byte(k.SHA256), []byte(digest)) == 1 {
				found, scopes = true, k.Scopes
				break
			}
		}
		if !found {
			fail(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
			return
		}
		if snap.Settings.SigningKey != "" {
			body, err := io.ReadAll(io.LimitReader(r.Body, channels.MaxBody))
			if err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "unreadable body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if !VerifySignature(snap.Settings.SigningKey, r.Header.Get("X-Ledgerbot-Signature"), body, time.Now(), 5*time.Minute) {
				fail(w, http.StatusUnauthorized, "bad_signature", "request signature is invalid or expired")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxSnap, snap)
		ctx = context.WithValue(ctx, ctxKeyScopes, scopes)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// VerifySignature checks "t=<unix>,v1=<hex hmac(secret, t + '.' + body)>".
func VerifySignature(secret, header string, body []byte, now time.Time, skew time.Duration) bool {
	return sign.Verify(secret, header, body, now, skew)
}

func scope(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scopes, _ := r.Context().Value(ctxKeyScopes).([]string)
			for _, sc := range scopes {
				if sc == "*" || sc == name {
					next.ServeHTTP(w, r)
					return
				}
			}
			fail(w, http.StatusForbidden, "forbidden", "API key lacks scope "+name)
		})
	}
}

// rateLimit applies a per-company token bucket in this process. With N API
// replicas behind a balancer the effective limit is roughly N × RateRPS.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.RateRPS <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		c := snapFrom(r).Settings.CompanyID
		s.limMu.Lock()
		l, found := s.limiters[c]
		if !found {
			l = rate.NewLimiter(rate.Limit(s.RateRPS), max(s.RateBurst, 1))
			s.limiters[c] = l
		}
		s.limMu.Unlock()
		if !l.Allow() {
			w.Header().Set("Retry-After", "1")
			fail(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// idempotency replays the stored response for a repeated Idempotency-Key.
func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 200 {
			fail(w, http.StatusBadRequest, "bad_request", "Idempotency-Key too long")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, channels.MaxBody))
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(append([]byte(r.URL.Path+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		c := snapFrom(r).Settings.CompanyID
		if rec, found, err := s.Store.GetIdem(r.Context(), c, key); err == nil && found {
			if rec.Hash != hash {
				fail(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "Idempotency-Key was used with a different request")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(rec.Status)
			_, _ = w.Write(rec.Body)
			return
		}
		sw := &statusWriter{ResponseWriter: w, buf: &bytes.Buffer{}}
		next.ServeHTTP(sw, r)
		if sw.status > 0 && sw.status < 500 {
			if err := s.Store.PutIdem(r.Context(), c, key, store.IdemRecord{Status: sw.status, Body: sw.buf.Bytes(), Hash: hash}, 24*time.Hour); err != nil {
				s.Log.Warn("idempotency store failed", "err", err)
			}
		}
	})
}

func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.AdminToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.AdminToken)) != 1 {
			fail(w, http.StatusUnauthorized, "unauthorized", "admin token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Ready(ctx); err != nil {
			fail(w, http.StatusServiceUnavailable, "not_ready", err.Error())
			return
		}
	}
	ok(w, http.StatusOK, map[string]string{"status": "ready"})
}
