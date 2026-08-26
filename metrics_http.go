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
// request with the app that served it, the product (route group) and the
// validated org, so a per-app and per-org request/error/latency series exists.
//
// `app` is the SUBSYSTEM the fleet delivers the path to, and it is not the same
// question as `product`: product is the first segment of the address, while app
// is who answers it — and for the subsystem holding the bare /v1 remainder the
// two are never equal. It is resolved by the same index the request span already
// stamps hanzo.subsystem from (middleware_tracing.go), so the metric and the
// trace name the same subsystem or neither does.
//
// CARDINALITY. `app` is bounded by the manifest's own rows; `product` is bounded
// by the finite route table; `status` is bounded to four classes
// (2xx/3xx/4xx/5xx); `org` is the VALIDATED IAM owner claim, NOT a client-chosen
// string — so a caller cannot inflate cardinality with arbitrary values (an
// unauthenticated request folds to a single "-" bucket). Cardinality is bounded
// by real orgs × products, the same envelope the eval per-org limiter reasons
// about.
//
// There is no `route` dimension and there must not be: the route this middleware
// holds is the REQUEST PATH, ids and all, so a per-route series would be one
// series per object this fleet has ever been asked about. The path lives on the
// span, where one row is one request and cardinality is not a standing cost.

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
			metric.WithDescription("Total /v1 requests by serving app, product (route group), validated org, and status class."))
		httpDuration, _ = m.Float64Histogram("hanzo_http_request_duration_seconds",
			metric.WithDescription("/v1 request duration in seconds by serving app, product (route group) and validated org."),
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
// (empty ⇒ "-"). app is the serving subsystem, product the route group; status is
// folded to its class to bound cardinality.
func observeRequest(app, product, org string, status int, dur time.Duration) {
	if product == "" {
		product = "unknown"
	}
	a := app
	if a == "" {
		a = "-"
	}
	o := org
	if o == "" {
		o = "-"
	}
	reqs, hist := instruments()
	if reqs != nil {
		reqs.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("app", a),
			attribute.String("product", product),
			attribute.String("org", o),
			attribute.String("status", statusClass(status)),
		))
	}
	if hist != nil {
		hist.Record(context.Background(), dur.Seconds(), metric.WithAttributes(
			attribute.String("app", a),
			attribute.String("product", product),
			attribute.String("org", o),
		))
	}
}

// seedApp writes 0 to the request series an app's own traffic will land on, so
// the series EXISTS the moment the app is mounted rather than the moment
// somebody calls it.
//
// ZERO IS A MEASUREMENT, and this is the same argument metrics_plane.go makes at
// length: a counter that springs into existence on its first Add cannot express
// "this stopped". Absent is what a metric store shows for an app that was never
// deployed, an app that failed to mount, and a typo — so a rule written against
// it either never fires or fires everywhere. Seeded, `increase(...[30m]) == 0`
// becomes a true sentence about a mounted app, and a restart reads as "still
// nothing", which is exactly right.
//
// TWO CLASSES, NOT FOUR. 2xx is what makes "this app stopped serving" a question
// with an answer. 5xx is what separates "no errors" from "nothing is even
// trying" — the same reason the alert egress seeds only its failing side. A 3xx
// or a 4xx appearing for the first time is unambiguous and needs no floor.
//
// product is the app's own name, which is what its addresses yield for every
// subsystem whose prefix is /v1/<name>. The one that claims the bare remainder
// answers under the products of the paths it receives, so its seeded pair holds
// at zero — it is a member of the per-app sum and contributes nothing to it,
// which is the only reading anyone takes.
func seedApp(name string) {
	if name == "" {
		return
	}
	reqs, _ := instruments()
	if reqs == nil {
		return
	}
	for _, class := range []string{"2xx", "5xx"} {
		reqs.Add(context.Background(), 0, metric.WithAttributes(
			attribute.String("app", name),
			attribute.String("product", name),
			attribute.String("org", "-"),
			attribute.String("status", class),
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
	if before, _, ok := strings.Cut(rest, "/"); ok {
		return before
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
