package cloud

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Per-request instruments, pushed to the telemetry store in-process.
//
// Their way out has moved twice and landed back where it started, which is worth
// recording so it does not move a third time. They were pushed over ZAP to match
// traces and logs; that was reverted to a scrape because the store every metric
// READER queried was VictoriaMetrics, and signals travel to where they are kept.
// VictoriaMetrics is gone and the datastore is the only store left, so the
// instruments below are collected into a registry and written to it directly
// (telemetry.go installs the reader, apps/o11y/metricspush.go drains it). The
// instruments themselves have never changed.
//
// This is the emit side of the per-org observability story: cloud tags every /v1
// request with the product (route group) and the validated org, so a per-org
// request/error/latency series exists keyed by {product,org}.
//
// CARDINALITY. `product` is bounded by the finite route table; `status` is
// bounded to four classes (2xx/3xx/4xx/5xx); `org` is the VALIDATED IAM owner
// claim, NOT a client-chosen string — so a caller cannot inflate cardinality
// with arbitrary values (an unauthenticated request folds to a single "-"
// bucket). Cardinality is bounded by real orgs × products, the same envelope the
// eval per-org limiter reasons about.

const meterName = "github.com/hanzoai/cloud"

var (
	instrumentsOnce sync.Once
	httpRequests    metric.Int64Counter
	httpDuration    metric.Float64Histogram
)

// instruments resolves the meter lazily.
//
// The meter provider is installed by the composition root. Resolving at init
// would bind to the no-op provider that exists before it runs, and every
// measurement afterwards would go nowhere while looking perfectly healthy.
func instruments() (metric.Int64Counter, metric.Float64Histogram) {
	instrumentsOnce.Do(func() {
		m := otel.Meter(meterName)
		httpRequests, _ = m.Int64Counter("hanzo_http_requests_total",
			metric.WithDescription("Total /v1 requests by product (route group), validated org, and status class."))
		httpDuration, _ = m.Float64Histogram("hanzo_http_request_duration_seconds",
			metric.WithDescription("/v1 request duration in seconds by product (route group) and validated org."),
			metric.WithUnit("s"))

		// cloud_up needs no writer: a gauge that reads 1 for as long as the
		// process serves is answered at collection time, not tracked.
		if up, err := m.Int64ObservableGauge("cloud_up",
			metric.WithDescription("1 if the process is serving.")); err == nil {
			_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
				o.ObserveInt64(up, 1)
				return nil
			}, up)
		}
	})
	return httpRequests, httpDuration
}

// observeRequest records one /v1 request. Called from the request-observability
// middleware AFTER the handler runs, so org reflects the validated principal
// (empty ⇒ "-"). product is the route group; status is folded to its class to
// bound cardinality.
func observeRequest(product, org string, status int, dur time.Duration) {
	if product == "" {
		product = "unknown"
	}
	o := org
	if o == "" {
		o = "-"
	}
	reqs, hist := instruments()
	if reqs != nil {
		reqs.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("product", product),
			attribute.String("org", o),
			attribute.String("status", statusClass(status)),
		))
	}
	if hist != nil {
		hist.Record(context.Background(), dur.Seconds(), metric.WithAttributes(
			attribute.String("product", product),
			attribute.String("org", o),
		))
	}
}

// productFromPath resolves the product (route group) from a /v1/<group>/... path:
// the first segment after /v1/. "/v1/agents/foo" → "agents"; "/v1/o11y/logs" →
// "o11y"; "/v1" or "/v1/" → "" (folds to "unknown" at emit).
func productFromPath(path string) string {
	rest := strings.TrimPrefix(path, "/v1/")
	if rest == path { // no /v1/ prefix
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// statusClass folds an HTTP status into its class label, bounding cardinality.
func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "other"
	}
}
