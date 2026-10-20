package assistant

import (
	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/metrics"
)

func observeAI(purpose string, u ai.Usage) {
	metrics.AICalls.WithLabelValues(purpose, u.Provider, u.Model).Inc()
	metrics.AITokens.WithLabelValues(purpose, u.Provider, "in").Add(float64(u.InputTokens))
	metrics.AITokens.WithLabelValues(purpose, u.Provider, "out").Add(float64(u.OutputTokens))
	if u.Latency > 0 {
		metrics.AILatency.WithLabelValues(purpose, u.Provider).Observe(u.Latency.Seconds())
	}
}
