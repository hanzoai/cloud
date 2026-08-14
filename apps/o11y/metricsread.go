package o11y

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// metricsread.go serves GET /v1/o11y/metrics — REAL per-org RED (rate / errors /
// latency) for a product, plus the org's LLM usage. The RED series come from the
// tenant's request spans in event.span (org, the plane's first sort-key column —
// stamped from the hanzo.org attribute cloud's TracingMiddleware writes) — so this
// is genuine per-tenant data, not the fleet's infra gauges (those carry no org
// label; availability.go reads those). Usage comes from the hanzo.cloud_usage
// ledger (organization=<org>).
//
// TENANT ISOLATION: org is the validated tenant, bound as a positional datastore
// parameter (never interpolated), the FIRST predicate on every query. A tenant can
// only ever aggregate its own spans/usage. The client picks a PRODUCT (validated +
// allowlisted via resolveService) and a bounded RANGE — never the query. A validated
// SuperAdmin (admin) is not org-pinned on the RED span query, so the platform view
// aggregates the whole product; usage is always the caller's own org.

const (
	defaultRangeSec = 3600   // 1h
	maxRangeSec     = 604800 // 7d
	minStepSec      = 30
	maxStepSec      = 3600
	metricsTimeout  = 10 * time.Second
)

type point struct {
	// T is the bucket start, RFC3339 in UTC.
	T string `json:"t"`
	// V is the bucket's value.
	V float64 `json:"v"`
}

// usageBucket is one time bucket of an org's LLM usage. NOT admin's usagePoint
// (a DAILY {date,requests,spendCents,tokens} roll-up on the fleet board): these
// are two different shapes, and the fleet document has ONE schema namespace, so
// they must not share a name — the weave gate refuses it, because a generated
// SDK would bind whichever it read last.
type usageBucket struct {
	// T is the bucket start, RFC3339 in UTC.
	T string `json:"t"`
	// Calls is how many LLM calls landed in the bucket.
	Calls int64 `json:"calls"`
	// Tokens is how many tokens they consumed.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost, in cents.
	CostCents int64 `json:"costCents"`
}

// metricsResponse is the scoped RED-metrics + usage response for one product.
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
		Calls     int64         `json:"calls"`
		Tokens    int64         `json:"tokens"`
		CostCents int64         `json:"costCents"`
		Series    []usageBucket `json:"series"`
	} `json:"usage"`
	Summary struct {
		Requests  int64   `json:"requests"`
		Errors    int64   `json:"errors"`
		ErrorRate float64 `json:"errorRate"`
		P95Ms     float64 `json:"p95Ms"`
	} `json:"summary"`
}

// metricsQuery is the resolved, already-authorized query for one product's series:
// the org is the validated tenant, the product an allowlisted service, the window
// clamped. No field is client-controlled raw SQL/PromQL.
type metricsQuery struct {
	svc      service
	org      string
	admin    bool
	rangeSec int
	stepSec  int
}

// queryMetrics returns the product's RED series + the org's LLM usage for the
// window. An unwired datastore is an honest 503 at the handler; here the datastore
// is known-enabled (the handler checks). The usage read is a secondary signal — a
// miss is surfaced as an error the handler logs, never a failed response.
func queryMetrics(ctx context.Context, q metricsQuery) (metricsResponse, error) {
	var resp metricsResponse
	resp.Product = q.svc.ID
	resp.Range.SinceSec = q.rangeSec
	resp.Range.StepSec = q.stepSec

	if err := redSeries(ctx, q, &resp); err != nil {
		return metricsResponse{}, err
	}
	// Usage is filled separately by the handler so a usage miss (secondary LLM
	// ledger) does not fail the RED response. ensureSeries has already run.
	return resp, nil
}

// redSeries fills the rate/errors/latency buckets from the tenant's request spans
// on the plane (event.span). A non-admin is pinned to its own org; an admin sees
// the whole product.
func redSeries(ctx context.Context, q metricsQuery, resp *metricsResponse) error {
	// One product can serve SEVERAL subtrees (account serves /v1/orgs and five
	// more), so the route predicate is built per prefix and OR'd. Every prefix
	// still rides as a BOUND PARAMETER — the product param reached here through
	// validProduct and an allowlist, and the manifest is our own table, but the
	// rule that no name is interpolated into SQL is the reason neither of those
	// has to be re-argued at this line.
	routeSQL := make([]string, 0, len(q.svc.Routes))
	args := []any{q.stepSec}
	for _, p := range q.svc.Routes {
		routeSQL = append(routeSQL, "attributes['http.route'] = ? OR startsWith(attributes['http.route'], ?)")
		args = append(args, p, strings.TrimSuffix(p, "/")+"/")
	}
	routeSQL = append(routeSQL, "service = ?")
	args = append(args, q.svc.App)

	// Longest prefix wins, exactly as the router decided it. Without this an app
	// that owns an ancestor path (ai owns /v1) counts every nested app's traffic as
	// its own. See nestedPrefixes in productmap.go.
	where := "(" + strings.Join(routeSQL, " OR ") + ")"
	if len(q.svc.Excludes) > 0 {
		ex := make([]string, 0, len(q.svc.Excludes))
		for _, p := range q.svc.Excludes {
			ex = append(ex, "attributes['http.route'] = ? OR startsWith(attributes['http.route'], ?)")
			args = append(args, p, p+"/")
		}
		where += " AND NOT (" + strings.Join(ex, " OR ") + ")"
	}
	args = append(args, q.rangeSec)

	sql := "SELECT toStartOfInterval(time, toIntervalSecond(?)) AS bucket, " +
		"count() AS reqs, " +
		// The HTTP status is a span attribute (Map values are strings); coerce
		// before the numeric compare. status is the span's own status column.
		"countIf(toInt32OrZero(attributes['http.response.status_code']) >= 500 OR status = 'error') AS errs, " +
		"quantile(0.5)(duration) AS p50, " +
		"quantile(0.95)(duration) AS p95 " +
		"FROM event.span " +
		"WHERE " + where + " " +
		"AND time > now64(9) - toIntervalSecond(?)"
	// THE tenant gate. A non-admin is pinned to its own org (the plane's first
	// sort-key column); a validated SuperAdmin sees the whole product (no org
	// predicate).
	if !q.admin {
		sql += " AND org = ?"
		args = append(args, q.org)
	}
	sql += " GROUP BY bucket ORDER BY bucket ASC"

	rows, err := datastore.Query(ctx, sql, args...)
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
// ledger. organization is bound (never interpolated) and is ALWAYS the caller's own
// org — usage is not a platform-wide view even for an admin. A LIMIT-free GROUP BY
// is safe because the time predicate bounds the scan.
func usageSeries(ctx context.Context, org string, rangeSec, stepSec int, resp *metricsResponse) error {
	sql := "SELECT toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket, " +
		"count() AS calls, sum(total_tokens) AS tokens, " + datastore.Spend + " AS cost " +
		"FROM hanzo.cloud_usage WHERE organization = ? " +
		"AND timestamp > now() - toIntervalSecond(?) " +
		"GROUP BY bucket ORDER BY bucket ASC"
	rows, err := datastore.Query(ctx, sql, stepSec, org, rangeSec)
	if err != nil {
		return fmt.Errorf("query usage: %w", err)
	}
	for _, r := range rows {
		t := asTime(r["bucket"]).UTC().Format(time.RFC3339)
		calls := asInt64(r["calls"])
		tokens := asInt64(r["tokens"])
		cost := asInt64(r["cost"])
		resp.Usage.Series = append(resp.Usage.Series, usageBucket{T: t, Calls: calls, Tokens: tokens, CostCents: cost})
		resp.Usage.Calls += calls
		resp.Usage.Tokens += tokens
		resp.Usage.CostCents += cost
	}
	if resp.Usage.Series == nil {
		resp.Usage.Series = []usageBucket{}
	}
	return nil
}

// ── range/step + helpers ──────────────────────────────────────────────────────

// boundRangeSec clamps the client `range` (seconds) to [1, maxRangeSec]. A
// missing or unparseable query value arrives as 0 and takes the default — the
// same branch a malformed string took when this parsed the query itself.
func boundRangeSec(n int) int {
	if n <= 0 {
		return defaultRangeSec
	}
	if n > maxRangeSec {
		return maxRangeSec
	}
	return n
}

// stepFor picks a bucket width: an explicit stepSec (clamped), else ~60 buckets
// across the range (clamped to [minStepSec, maxStepSec]).
func stepFor(rangeSec, step int) int {
	if step > 0 {
		return clampInt(step, minStepSec, maxStepSec)
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
	if resp.Usage.Series == nil {
		resp.Usage.Series = []usageBucket{}
	}
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
func round4(f float64) float64 { return float64(int64(f*10000+0.5)) / 10000 }
