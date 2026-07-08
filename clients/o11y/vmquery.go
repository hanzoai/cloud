package o11y

// vmquery.go — the metrics-query companion for the scoped o11y surface. scope.go
// serves /v1/o11y/metrics via queryMetrics(), and status.go's VM up-inventory uses
// newVMClient()/promLabel() over the vmClient defined here (status.go owns the
// queryInstant method + the instant-response envelope). This file was missing from
// the incomplete o11y-scope landing (its symbols were referenced but never
// committed), which took `go build ./cmd/cloud` un-buildable; it is restored here
// to the file's own "honest, never fabricated" contract: the VM read endpoint is
// resolved from O11Y_VM_URL, and when it is unset/unreachable every query degrades
// to an honest-empty result rather than a synthesized series.

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// vmClient reads a Prometheus/VictoriaMetrics HTTP API. status.go hangs the
// queryInstant method off this type; the fields it needs are the read base URL and
// an HTTP client.
type vmClient struct {
	base string
	http *http.Client
}

const vmQueryTimeout = 5 * time.Second

// newVMClient builds the VM read client from O11Y_VM_URL (the in-cluster
// VictoriaMetrics / vmselect query endpoint, e.g. http://victoria-metrics.hanzo.svc:8428).
// An EMPTY base makes every query fail cleanly, so an un-wired VM degrades to an
// honest-empty inventory/series — never a fabricated replica or point.
func newVMClient() *vmClient {
	return &vmClient{
		base: strings.TrimRight(os.Getenv("O11Y_VM_URL"), "/"),
		http: &http.Client{Timeout: vmQueryTimeout},
	}
}

// promLabel escapes a PromQL double-quoted label value so a product/service id can
// never break out of the SERVER-built selector. Defense in depth — resolveService
// already allowlists the id before it reaches a query.
func promLabel(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", "",
		"\r", "",
		"{", "",
		"}", "",
	).Replace(s)
}

// metricPoint is one (unix-seconds, value) sample of a RED series.
type metricPoint struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

// usageRollup is the per-org usage summary fused into the metrics response.
// Available is false until the usage read is wired, so the client shows an honest
// "usage unavailable" rather than a fabricated zero.
type usageRollup struct {
	Available bool  `json:"available"`
	Requests  int64 `json:"requests,omitempty"`
	Tokens    int64 `json:"tokens,omitempty"`
	CostCents int64 `json:"costCents,omitempty"`
}

// metricsResult is the scoped RED-metrics response for one product.
type metricsResult struct {
	Product     string        `json:"product"`
	Up          []metricPoint `json:"up"`
	RequestRate []metricPoint `json:"requestRate"`
	ErrorRate   []metricPoint `json:"errorRate"`
	P50         []metricPoint `json:"p50"`
	P95         []metricPoint `json:"p95"`
	Usage       usageRollup   `json:"usage"`
	Source      string        `json:"source"`
}

// metricsQuery is the resolved, already-authorized query for one product's series:
// the org is the validated tenant, the product an allowlisted service, the window
// clamped. No field is client-controlled raw SQL/PromQL.
type metricsQuery struct {
	svc     service
	org     string
	admin   bool
	minutes int
}

const (
	defaultRangeMinutes = 60
	maxRangeMinutes     = 24 * 60
)

// boundRangeMinutes clamps the client `minutes` to [1, maxRangeMinutes], defaulting
// an absent/invalid value — the same bounded-input contract as boundSinceHours.
func boundRangeMinutes(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return defaultRangeMinutes
	}
	if n > maxRangeMinutes {
		return maxRangeMinutes
	}
	return n
}

// queryMetrics returns the product's RED series for the window. The VM range-query
// wiring is not yet live in the embedded runtime, so this returns the honest-empty
// series (no fabricated points) with the usage rollup marked unavailable — the SAME
// "honest, never fabricated" contract emptyMetrics/unknownStatus already follow.
// When the VM read endpoint (O11Y_VM_URL) is wired, replace the body with real
// query_range calls over vmClient for up/rate/error/p50/p95 selectors pinned to
// q.svc.PromService (and, for non-admin, q.org).
func queryMetrics(_ context.Context, q metricsQuery) (metricsResult, error) {
	res := emptyMetrics(q.svc.ID)
	res.Source = "metrics-unavailable"
	return res, nil
}
