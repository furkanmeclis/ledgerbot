// Package metrics exposes Prometheus collectors. Labels are kept low-cardinality:
// company ids are never used as labels (use the usage report API for that).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_http_requests_total", Help: "HTTP requests by route and status class.",
	}, []string{"route", "method", "code"})
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ledgerbot_http_request_duration_seconds", Help: "HTTP latency by route.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route"})
	MessagesHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_messages_total", Help: "Inbound messages handled by channel and kind.",
	}, []string{"channel", "kind"})
	HandleDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ledgerbot_handle_duration_seconds", Help: "End-to-end message handling latency.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2.5, 12),
	}, []string{"channel"})
	AICalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_ai_calls_total", Help: "AI provider calls by purpose and provider.",
	}, []string{"purpose", "provider", "model"})
	AITokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_ai_tokens_total", Help: "AI tokens by direction.",
	}, []string{"purpose", "provider", "direction"})
	AILatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ledgerbot_ai_latency_seconds", Help: "AI provider latency.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
	}, []string{"purpose", "provider"})
	JobsProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_jobs_total", Help: "Worker jobs by kind and outcome.",
	}, []string{"kind", "outcome"})
	JobsSnoozed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_jobs_snoozed_total", Help: "Jobs snoozed by the per-company fairness limiter.",
	}, []string{"kind"})
	LeasesHeld = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ledgerbot_channel_leases_held", Help: "Channel listener leases held by this process.",
	})
	WebhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledgerbot_webhook_deliveries_total", Help: "Outbound webhook deliveries by outcome.",
	}, []string{"outcome"})
)
