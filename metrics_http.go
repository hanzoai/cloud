package cloud

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Per-request Prometheus instruments, exposed on the ops /metrics listener so the
// cloud ServiceMonitor scrapes them into VictoriaMetrics. This is the emit side of
// the per-org observability story: it is what makes the scoped /v1/o11y/metrics
// RED series REAL going forward — cloud tags every /v1 request with the product
// (route group) and the validated org, so a per-org request/error/latency series
// exists in VM keyed by {product,org}.
//
// CARDINALITY. `product` is bounded by the finite route table; `status` is bounded
// to four classes (2xx/3xx/4xx/5xx); `org` is the VALIDATED IAM owner claim, NOT a
// client-chosen string — so a caller cannot inflate cardinality with arbitrary
// values (an unauthenticated request folds to a single "-" bucket). Cardinality is
// therefore bounded by real orgs × products, the same envelope the eval
// per-org limiter reasons about.
//
// The registry is private and self-contained (no global default registry), so the
// ops /metrics surface never shares failure modes with the product API stack.

var (
	metricsRegistry = prometheus.NewRegistry()

	cloudUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cloud_up",
		Help: "1 if the process is serving.",
	})

	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hanzo_http_requests_total",
		Help: "Total /v1 requests by product (route group), validated org, and status class.",
	}, []string{"product", "org", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hanzo_http_request_duration_seconds",
		Help:    "/v1 request duration in seconds by product (route group) and validated org.",
		Buckets: prometheus.DefBuckets,
	}, []string{"product", "org"})
)

func init() {
	cloudUp.Set(1)
	metricsRegistry.MustRegister(cloudUp, httpRequestsTotal, httpRequestDuration)
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
	httpRequestsTotal.WithLabelValues(product, o, statusClass(status)).Inc()
	httpRequestDuration.WithLabelValues(product, o).Observe(dur.Seconds())
}

// MetricsHandler serves the private registry in Prometheus exposition format for
// the ops /metrics listener (healthMux). Self-contained: it reads only this
// registry, never the API stack.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
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
