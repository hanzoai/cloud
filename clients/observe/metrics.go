package observe

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// metrics.go serves GET /v1/o11y/metrics — REAL per-org RED (rate / errors /
// latency) for a product, plus the org's LLM usage. The RED series come from
// org-tagged request spans in signoz_traces (attributes_string['hanzo.org']),
// which the cloud TracingMiddleware already stamps — so this is genuine per-tenant
// data, not VictoriaMetrics infra metrics (those carry no org label). Usage comes
// from the hanzo.cloud_usage ledger (organization=<org>).
//
// TENANT ISOLATION: org is principal.Tenant(c), bound as a positional ClickHouse
// parameter (never interpolated), the FIRST predicate on every query. A tenant can
// only ever aggregate its own spans/usage. The client picks a PRODUCT (validated)
// and a bounded RANGE — never the query.

const (
	defaultRangeSec = 3600    // 1h
	maxRangeSec     = 604800  // 7d
	minStepSec      = 30
	maxStepSec      = 3600
	metricsTimeout  = 10 * time.Second
)

type point struct {
	T string  `json:"t"` // RFC3339 bucket start (UTC)
	V float64 `json:"v"`
}

type usagePoint struct {
	T         string `json:"t"`
	Calls     int64  `json:"calls"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

type metricsResponse struct {
	Product string `json:"product"`
	Range   struct {
		SinceSec int `json:"sinceSec"`
		StepSec  int `json:"stepSec"`
	} `json:"range"`
	Series struct {
		Requests     []point `json:"requests"`
		Errors       []point `json:"errors"`
		LatencyP50Ms []point `json:"latencyP50Ms"`
		LatencyP95Ms []point `json:"latencyP95Ms"`
	} `json:"series"`
	Usage struct {
		Calls     int64        `json:"calls"`
		Tokens    int64        `json:"tokens"`
		CostCents int64        `json:"costCents"`
		Series    []usagePoint `json:"series"`
	} `json:"usage"`
	Summary struct {
		Requests  int64   `json:"requests"`
		Errors    int64   `json:"errors"`
		ErrorRate float64 `json:"errorRate"`
		P95Ms     float64 `json:"p95Ms"`
	} `json:"summary"`
}

func (s *service) getMetrics(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProductQuery(c)
	if err != nil {
		return err
	}
	if !aiobject.DatastoreEnabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "o11y metrics: datastore not connected")
	}
	service := serviceFor(product)
	rangeSec := parseRangeSec(c)
	stepSec := stepFor(rangeSec, c)

	ctx, cancel := ctxWithTimeout(c.Context(), metricsTimeout)
	defer cancel()

	var resp metricsResponse
	resp.Product = product
	resp.Range.SinceSec = rangeSec
	resp.Range.StepSec = stepSec

	if err := s.redSeries(ctx, org, product, service, rangeSec, stepSec, &resp); err != nil {
		return zip.Errorf(http.StatusBadGateway, "o11y metrics: %v", err)
	}
	if err := s.usageSeries(ctx, org, rangeSec, stepSec, &resp); err != nil {
		// Usage is a secondary signal (LLM ledger); a miss is logged, never fails
		// the whole tab — the RED series still render.
		s.log.Warn("o11y metrics: usage series unavailable", "org", org, "err", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// redSeries fills the rate/errors/latency buckets from org-tagged request spans.
func (s *service) redSeries(ctx context.Context, org, product, service string, rangeSec, stepSec int, resp *metricsResponse) error {
	routePrefix := "/v1/" + product
	q := "SELECT toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket, " +
		"count() AS reqs, " +
		"countIf(response_status_code >= 500 OR status_code = 2) AS errs, " +
		"quantile(0.5)(duration_nano) AS p50, " +
		"quantile(0.95)(duration_nano) AS p95 " +
		"FROM signoz_traces.distributed_signoz_index_v3 " +
		"WHERE attributes_string['hanzo.org'] = ? " +
		"AND (httpRoute = ? OR startsWith(httpRoute, ?) OR serviceName = ?) " +
		"AND timestamp > now64() - toIntervalSecond(?) " +
		"GROUP BY bucket ORDER BY bucket ASC"
	args := []any{stepSec, org, routePrefix, routePrefix + "/", service, rangeSec}

	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query RED: %w", err)
	}
	var totReq, totErr int64
	var maxP95 float64
	for _, r := range rows {
		t := asTime(r["bucket"]).UTC().Format(time.RFC3339)
		reqs := asInt64(r["reqs"])
		errs := asInt64(r["errs"])
		p50 := asFloat(r["p50"]) / 1e6 // ns → ms
		p95 := asFloat(r["p95"]) / 1e6
		resp.Series.Requests = append(resp.Series.Requests, point{T: t, V: float64(reqs)})
		resp.Series.Errors = append(resp.Series.Errors, point{T: t, V: float64(errs)})
		resp.Series.LatencyP50Ms = append(resp.Series.LatencyP50Ms, point{T: t, V: round2(p50)})
		resp.Series.LatencyP95Ms = append(resp.Series.LatencyP95Ms, point{T: t, V: round2(p95)})
		totReq += reqs
		totErr += errs
		if p95 > maxP95 {
			maxP95 = p95
		}
	}
	resp.Summary.Requests = totReq
	resp.Summary.Errors = totErr
	if totReq > 0 {
		resp.Summary.ErrorRate = round4(float64(totErr) / float64(totReq))
	}
	resp.Summary.P95Ms = round2(maxP95)
	// Never return a null series — the console renders an empty chart, not a crash.
	ensureSeries(resp)
	return nil
}

// usageSeries fills the org's LLM usage (calls/tokens/cost) from the cloud_usage
// ledger. organization is bound (never interpolated); a LIMIT-free GROUP BY is safe
// because the time predicate bounds the scan.
func (s *service) usageSeries(ctx context.Context, org string, rangeSec, stepSec int, resp *metricsResponse) error {
	q := "SELECT toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket, " +
		"count() AS calls, sum(total_tokens) AS tokens, sum(cost_cents) AS cost " +
		"FROM hanzo.cloud_usage WHERE organization = ? " +
		"AND timestamp > now() - toIntervalSecond(?) " +
		"GROUP BY bucket ORDER BY bucket ASC"
	rows, err := aiobject.DatastoreQuery(ctx, q, stepSec, org, rangeSec)
	if err != nil {
		return fmt.Errorf("query usage: %w", err)
	}
	for _, r := range rows {
		t := asTime(r["bucket"]).UTC().Format(time.RFC3339)
		calls := asInt64(r["calls"])
		tokens := asInt64(r["tokens"])
		cost := asInt64(r["cost"])
		resp.Usage.Series = append(resp.Usage.Series, usagePoint{T: t, Calls: calls, Tokens: tokens, CostCents: cost})
		resp.Usage.Calls += calls
		resp.Usage.Tokens += tokens
		resp.Usage.CostCents += cost
	}
	if resp.Usage.Series == nil {
		resp.Usage.Series = []usagePoint{}
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func parseRangeSec(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("range")))
	if err != nil || n <= 0 {
		return defaultRangeSec
	}
	if n > maxRangeSec {
		return maxRangeSec
	}
	return n
}

// stepFor picks a bucket width: an explicit ?stepSec (clamped), else ~60 buckets
// across the range (clamped to [minStepSec, maxStepSec]).
func stepFor(rangeSec int, c *zip.Ctx) int {
	if v := strings.TrimSpace(c.Query("stepSec")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return clampInt(n, minStepSec, maxStepSec)
		}
	}
	return clampInt(rangeSec/60, minStepSec, maxStepSec)
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func ensureSeries(resp *metricsResponse) {
	if resp.Series.Requests == nil {
		resp.Series.Requests = []point{}
	}
	if resp.Series.Errors == nil {
		resp.Series.Errors = []point{}
	}
	if resp.Series.LatencyP50Ms == nil {
		resp.Series.LatencyP50Ms = []point{}
	}
	if resp.Series.LatencyP95Ms == nil {
		resp.Series.LatencyP95Ms = []point{}
	}
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
func round4(f float64) float64 { return float64(int64(f*10000+0.5)) / 10000 }
