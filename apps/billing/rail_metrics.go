package billing

// rail_metrics.go records per-rail payment outcome counters at the /v1/billing HTTP
// boundary without ever logging or labelling credentials, tokens, client secrets, or keys.

import (
	"context"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type railCounters struct {
	mu       sync.Mutex
	outcomes map[string]int64
	once     sync.Once
	otelOut  metric.Int64Counter
}

var billingRailMetrics = &railCounters{outcomes: map[string]int64{}}

func (m *railCounters) record(rail, op, outcome string) {
	rail = safeMetricLabel(rail, "unknown")
	op = safeMetricLabel(op, "unknown")
	outcome = safeMetricLabel(outcome, "unknown")

	m.once.Do(func() {
		meter := otel.Meter("hanzo.cloud/billing.rails")
		m.otelOut, _ = meter.Int64Counter(
			"hanzo.billing.rail.outcomes",
			metric.WithDescription("Per-rail billing HTTP outcome counts (no sensitive fields)"),
		)
	})
	if m.otelOut != nil {
		m.otelOut.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("rail", rail),
			attribute.String("op", op),
			attribute.String("outcome", outcome),
		))
	}

	key := rail + ":" + op + ":" + outcome
	m.mu.Lock()
	m.outcomes[key]++
	m.mu.Unlock()
}

func (m *railCounters) count(rail, op, outcome string) int64 {
	key := safeMetricLabel(rail, "unknown") + ":" + safeMetricLabel(op, "unknown") + ":" + safeMetricLabel(outcome, "unknown")
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[key]
}

func safeMetricLabel(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 64 {
		return fallback
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return fallback
	}
	return s
}
