package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// series gathers the default registry and returns the metric of family name
// whose labels include every pair in labels.
func series(t *testing.T, name string, labels map[string]string) (counter, gauge float64, buckets map[float64]uint64, ok bool) {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue metric
				}
			}
			if h := m.GetHistogram(); h != nil {
				buckets = map[float64]uint64{}
				for _, b := range h.GetBucket() {
					buckets[b.GetUpperBound()] = b.GetCumulativeCount()
				}
			}
			return m.GetCounter().GetValue(), m.GetGauge().GetValue(), buckets, true
		}
	}
	return 0, 0, nil, false
}

func TestCollectorsRegistered(t *testing.T) {
	HTTPRequests.WithLabelValues("/reg", "GET", "2xx")
	HTTPDuration.WithLabelValues("/reg")
	MessagesHandled.WithLabelValues("dev", "text")
	HandleDuration.WithLabelValues("dev")
	AICalls.WithLabelValues("agent", "fake", "fake")
	AITokens.WithLabelValues("agent", "fake", "in")
	AILatency.WithLabelValues("agent", "fake")
	JobsProcessed.WithLabelValues("t", "ok")
	JobsSnoozed.WithLabelValues("t")
	WebhookDeliveries.WithLabelValues("ok")
	for _, name := range []string{
		"ledgerbot_http_requests_total", "ledgerbot_http_request_duration_seconds",
		"ledgerbot_messages_total", "ledgerbot_handle_duration_seconds",
		"ledgerbot_ai_calls_total", "ledgerbot_ai_tokens_total", "ledgerbot_ai_latency_seconds",
		"ledgerbot_jobs_total", "ledgerbot_jobs_snoozed_total",
		"ledgerbot_channel_leases_held", "ledgerbot_webhook_deliveries_total",
	} {
		if _, _, _, ok := series(t, name, nil); !ok {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func TestCounterLabels(t *testing.T) {
	cases := []struct {
		name   string
		inc    func()
		family string
		labels map[string]string
		want   float64
	}{
		{"http", func() { HTTPRequests.WithLabelValues("/ctr", "POST", "5xx").Add(3) }, "ledgerbot_http_requests_total",
			map[string]string{"route": "/ctr", "method": "POST", "code": "5xx"}, 3},
		{"messages", func() { MessagesHandled.WithLabelValues("whatsapp-ctr", "voice").Inc() }, "ledgerbot_messages_total",
			map[string]string{"channel": "whatsapp-ctr", "kind": "voice"}, 1},
		{"tokens", func() { AITokens.WithLabelValues("extraction", "ctr", "out").Add(128) }, "ledgerbot_ai_tokens_total",
			map[string]string{"purpose": "extraction", "provider": "ctr", "direction": "out"}, 128},
		{"jobs", func() { JobsProcessed.WithLabelValues("ctr", "error").Inc() }, "ledgerbot_jobs_total",
			map[string]string{"kind": "ctr", "outcome": "error"}, 1},
		{"webhooks", func() { WebhookDeliveries.WithLabelValues("ctr-ok").Add(2) }, "ledgerbot_webhook_deliveries_total",
			map[string]string{"outcome": "ctr-ok"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.inc()
			got, _, _, ok := series(t, c.family, c.labels)
			if !ok || got != c.want {
				t.Fatalf("%s%v = %v (found=%v), want %v", c.family, c.labels, got, ok, c.want)
			}
		})
	}
}

func TestWrongLabelCountPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("wrong label cardinality should panic")
		}
	}()
	HTTPRequests.WithLabelValues("only-one")
}

func TestGauge(t *testing.T) {
	LeasesHeld.Set(0)
	LeasesHeld.Inc()
	LeasesHeld.Inc()
	LeasesHeld.Dec()
	if _, g, _, ok := series(t, "ledgerbot_channel_leases_held", nil); !ok || g != 1 {
		t.Fatalf("LeasesHeld = %v", g)
	}
	LeasesHeld.Set(0)
}

func TestHTTPDurationBuckets(t *testing.T) {
	HTTPDuration.WithLabelValues("/bucket").Observe(0.003)
	_, _, b, ok := series(t, "ledgerbot_http_request_duration_seconds", map[string]string{"route": "/bucket"})
	if !ok || len(b) != 12 {
		t.Fatalf("buckets = %v", b)
	}
	for le, n := range b {
		want := uint64(0)
		if le >= 0.005 {
			want = 1
		}
		if n != want {
			t.Fatalf("bucket le=%v count=%d, want %d", le, n, want)
		}
	}
}

func TestExponentialBuckets(t *testing.T) {
	HandleDuration.WithLabelValues("bucket-test").Observe(1)
	AILatency.WithLabelValues("bucket-test", "fake").Observe(1)
	if _, _, b, _ := series(t, "ledgerbot_handle_duration_seconds", map[string]string{"channel": "bucket-test"}); len(b) != 12 {
		t.Fatalf("handle buckets = %d", len(b))
	}
	if _, _, b, _ := series(t, "ledgerbot_ai_latency_seconds", map[string]string{"purpose": "bucket-test"}); len(b) != 10 || b[25.6] != 1 || b[0.05] != 0 {
		t.Fatalf("ai latency buckets = %v", b)
	}
}
