// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	statusOK  = "ok"
	statusErr = "error"
)

// ── Shared envelope & metadata types ─────────────────────────────────────────

// Freshness reports data recency, lag, staleness, provenance, and precision.
type Freshness struct {
	// AsOf is the RFC3339 UTC timestamp when the returned snapshot was computed.
	AsOf string `json:"asOf"`
	// LagMs is the age of the snapshot in milliseconds relative to response time.
	LagMs int64 `json:"lagMs"`
	// Stale is true when served from a last-known Hanzo KV snapshot during a Datastore outage.
	Stale bool `json:"stale"`
	// Source identifies the primary upstream pipeline or storage layer.
	Source string `json:"source"`
	// Accuracy classifies metric precision: "exact", "approximate", or "estimated".
	Accuracy string `json:"accuracy"`
}

// PlanMeta summarizes the execution plan selected by the automatic query planner.
type PlanMeta struct {
	// Table is the coarsest sufficient pre-aggregated analytics.* table queried.
	Table string `json:"table"`
	// Window is the normalized lookback duration (e.g. "24h", "7d", "30d", "90d").
	Window string `json:"window"`
	// Granularity is the aggregation bucket selected ("minute", "hour", "day", or "snapshot").
	Granularity string `json:"granularity"`
	// Scope is the enforced IAM isolation scope ("global" or "org:<slug>").
	Scope string `json:"scope"`
	// Limit is the enforced row limit ceiling (default 100, max 500).
	Limit int `json:"limit"`
}

// ── GET /v1/analytics/metrics ────────────────────────────────────────────────

// MetricsIn filters the canonical metric catalog returned by GET /v1/analytics/metrics.
type MetricsIn struct {
	// Domain optionally filters metrics to one domain (commerce, ai, iam, o11y, compute, product).
	Domain string `json:"domain,omitempty"`
}

// MetricsData is the payload returned by GET /v1/analytics/metrics.
type MetricsData struct {
	// Metrics lists every registered metric definition matching the optional domain filter.
	Metrics []MetricDef `json:"metrics"`
	// Total is the count of metrics returned.
	Total int `json:"total"`
}

// MetricsOut is the GET /v1/analytics/metrics response envelope.
type MetricsOut struct {
	// Status is "ok" on success or "error" on failure.
	Status string `json:"status"`
	// Msg carries an error message when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data holds the registered metric catalog definitions.
	Data *MetricsData `json:"data,omitempty"`
}

// metrics lists all canonical registered metrics, their units, allowed group_by dimensions,
// upstream sources, freshness targets, accuracy classifications, and backing tables.
//
// Example: {"domain":"ai"}
// Response: {"status":"ok","msg":"","data":{"metrics":[{"id":"ai.tokens","domain":"ai","description":"Total prompt and completion tokens processed across AI models.","unit":"tokens","dimensions":["org","model","provider","day","hour"],"source":"ai_ledger","freshnessTargetSec":15,"accuracy":"exact","tables":["analytics.rollup_ai_hourly","analytics.rollup_ai_daily"]}],"total":1}}
func (s *Service) metrics(ctx context.Context, in *MetricsIn) (*MetricsOut, error) {
	if _, err := ResolveScope(ctx); err != nil {
		return nil, err
	}
	domain := ""
	if in != nil {
		domain = in.Domain
	}
	list := CatalogMetrics(domain)
	return &MetricsOut{
		Status: statusOK,
		Data: &MetricsData{
			Metrics: list,
			Total:   len(list),
		},
	}, nil
}

// ── POST /v1/analytics/query ─────────────────────────────────────────────────

// QueryIn is the structured analytical query accepted by POST /v1/analytics/query.
type QueryIn struct {
	// Metrics lists one or more registered canonical metric IDs (e.g. ["ai.requests", "ai.tokens"]).
	Metrics []string `json:"metrics"`
	// GroupBy lists up to 2 allowed dimension keys for this metric family (e.g. ["model", "day"]).
	GroupBy []string `json:"group_by,omitempty"`
	// Window is the lookback window (e.g. "24h", "7d", "30d", "90d", max "365d"). Defaults to "24h".
	Window string `json:"window,omitempty"`
	// Granularity selects "auto", "minute", "hour", or "day". Defaults to "auto" (coarsest sufficient table).
	Granularity string `json:"granularity,omitempty"`
	// Org optionally narrows a SuperAdmin query to one tenant org. Forbidden for non-super callers trying to read another org.
	Org string `json:"org,omitempty"`
	// Limit caps returned grouped rows (default 100, max 500).
	Limit int `json:"limit,omitempty"`
}

// QueryRow represents one grouped analytical result row with dimension keys and metric values.
type QueryRow struct {
	// Dimensions maps each requested group_by key to its string value for this row.
	Dimensions map[string]string `json:"dimensions,omitempty"`
	// Values maps each requested canonical metric ID to its numeric value for this row.
	Values map[string]float64 `json:"values"`
}

// QueryData is the payload returned by POST /v1/analytics/query.
type QueryData struct {
	// Rows lists the grouped analytical rows ordered by time or leading metric descending.
	Rows []QueryRow `json:"rows"`
	// Totals maps each requested canonical metric ID to its overall aggregate across the window.
	Totals map[string]float64 `json:"totals"`
	// Plan describes the aggregate table, window, granularity, scope, and limit chosen by the planner.
	Plan PlanMeta `json:"plan"`
	// Freshness reports snapshot timestamp, lag, stale degradation state, source, and accuracy.
	Freshness Freshness `json:"freshness"`
}

// QueryOut is the POST /v1/analytics/query response envelope.
type QueryOut struct {
	// Status is "ok" on success or "error" on failure.
	Status string `json:"status"`
	// Msg carries an error message when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data holds the query rows, totals, execution plan metadata, and freshness metadata.
	Data *QueryData `json:"data,omitempty"`
}

// query plans and executes a structured analytical query against Hanzo Datastore's
// pre-aggregated analytics.* tables with IAM tenant isolation, cardinality/cost budgets,
// positional SQL parameters, and Hanzo KV caching with stale-on-outage fallback.
//
// Example: {"metrics":["ai.requests","ai.tokens"],"group_by":["model"],"window":"7d","limit":20}
// Response: {"status":"ok","msg":"","data":{"rows":[{"dimensions":{"model":"zen-pro"},"values":{"ai.requests":420,"ai.tokens":125000}}],"totals":{"ai.requests":420,"ai.tokens":125000},"plan":{"table":"analytics.rollup_ai_hourly","window":"7d","granularity":"hour","scope":"global","limit":20},"freshness":{"asOf":"2026-11-25T00:00:00Z","lagMs":12,"stale":false,"source":"ai_ledger","accuracy":"exact"}}}
func (s *Service) query(ctx context.Context, in *QueryIn) (*QueryOut, error) {
	scope, err := ResolveScope(ctx)
	if err != nil {
		return nil, err
	}
	var req QueryIn
	if in != nil {
		req = *in
	}
	now := s.now()
	plan, err := planAt(req, scope, now)
	if err != nil {
		return nil, PlanErrorToZip(err)
	}

	cachePayload, _ := json.Marshal(struct {
		Table       string   `json:"t"`
		Window      string   `json:"w"`
		Granularity string   `json:"g"`
		Scope       string   `json:"s"`
		Metrics     []string `json:"m"`
		GroupBy     []string `json:"gb"`
		Limit       int      `json:"l"`
	}{
		Table:       plan.Table,
		Window:      plan.Window,
		Granularity: plan.Granularity,
		Scope:       plan.EffectiveScope.Key(),
		Metrics:     metricIDs(plan.Metrics),
		GroupBy:     plan.GroupBy,
		Limit:       plan.Limit,
	})
	cacheKey := CacheKey(plan.EffectiveScope.Key(), "query:"+string(cachePayload))

	type cachedQuery struct {
		Rows   []QueryRow         `json:"rows"`
		Totals map[string]float64 `json:"totals"`
	}

	res, meta, err := GetOrFetch(ctx, s.cache, cacheKey, func(fctx context.Context) (cachedQuery, error) {
		rawRows, qerr := s.backend.Query(fctx, plan.SQL, plan.Args...)
		if qerr != nil {
			return cachedQuery{}, qerr
		}
		rows := decodeQueryRows(rawRows, plan)
		totals := make(map[string]float64, len(plan.Metrics))

		if len(plan.GroupBy) == 0 {
			if len(rows) > 0 {
				totals = rows[0].Values
			} else {
				for _, m := range plan.Metrics {
					totals[m.ID] = 0
				}
			}
		} else if plan.TotalsSQL != "" {
			totRows, terr := s.backend.Query(fctx, plan.TotalsSQL, plan.TotalsArgs...)
			if terr == nil && len(totRows) > 0 {
				for _, m := range plan.Metrics {
					totals[m.ID] = rowFloat64(totRows[0][m.alias])
				}
			} else {
				// Fallback fold across returned rows if companion totals query returned no rows.
				for _, m := range plan.Metrics {
					var sum float64
					for _, r := range rows {
						sum += r.Values[m.ID]
					}
					totals[m.ID] = sum
				}
			}
		}

		return cachedQuery{Rows: rows, Totals: totals}, nil
	})
	if err != nil {
		return nil, PlanErrorToZip(err)
	}

	lagMs := max(int64(0), s.now().Sub(meta.AsOf).Milliseconds())
	return &QueryOut{
		Status: statusOK,
		Data: &QueryData{
			Rows:   res.Rows,
			Totals: res.Totals,
			Plan: PlanMeta{
				Table:       plan.Table,
				Window:      plan.Window,
				Granularity: plan.Granularity,
				Scope:       plan.EffectiveScope.Key(),
				Limit:       plan.Limit,
			},
			Freshness: Freshness{
				AsOf:     meta.AsOf.UTC().Format(time.RFC3339),
				LagMs:    lagMs,
				Stale:    meta.Stale,
				Source:   plan.Source,
				Accuracy: plan.Accuracy,
			},
		},
	}, nil
}

func metricIDs(defs []MetricDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

func decodeQueryRows(raw []map[string]any, plan QueryPlan) []QueryRow {
	out := make([]QueryRow, 0, len(raw))
	for _, r := range raw {
		qr := QueryRow{
			Values: make(map[string]float64, len(plan.Metrics)),
		}
		if len(plan.GroupBy) > 0 {
			qr.Dimensions = make(map[string]string, len(plan.GroupBy))
			for _, dim := range plan.GroupBy {
				qr.Dimensions[dim] = rowStr(r[dim])
			}
		}
		for _, m := range plan.Metrics {
			qr.Values[m.ID] = rowFloat64(r[m.alias])
		}
		out = append(out, qr)
	}
	return out
}

// ── GET /v1/analytics/overview ───────────────────────────────────────────────

// OverviewIn specifies optional parameters for GET /v1/analytics/overview.
type OverviewIn struct {
	// Org optionally narrows a SuperAdmin overview to one tenant org. Non-super callers are hard-scoped to their own org.
	Org string `json:"org,omitempty"`
	// Window is the lookback window for movement KPIs (e.g. "24h", "7d", "30d", max "365d"). Defaults to "30d".
	Window string `json:"window,omitempty"`
}

// OverviewKPIs holds the constant-cost executive KPIs read from analytics.global_overview or analytics.org_overview.
type OverviewKPIs struct {
	// RevenueCents is recognized revenue over the selected window in USD cents.
	RevenueCents int64 `json:"revenueCents"`
	// MRRCents is current run-rate Monthly Recurring Revenue in USD cents.
	MRRCents int64 `json:"mrrCents"`
	// ARRCents is current run-rate Annual Recurring Revenue (MRR * 12) in USD cents.
	ARRCents int64 `json:"arrCents"`
	// CashHeldCents is current unspent customer prepaid cash balance in USD cents.
	CashHeldCents int64 `json:"cashHeldCents"`
	// CreditOutstandingCents is current unspent granted credit balance in USD cents.
	CreditOutstandingCents int64 `json:"creditOutstandingCents"`
	// SpendCents is total metered spend over the selected window in USD cents.
	SpendCents int64 `json:"spendCents"`
	// AIRequests is total AI inference requests served over the selected window.
	AIRequests int64 `json:"aiRequests"`
	// AITokens is total AI tokens processed over the selected window.
	AITokens int64 `json:"aiTokens"`
	// AICostCents is upstream provider AI cost over the selected window in USD cents.
	AICostCents int64 `json:"aiCostCents"`
	// AIBilledCents is customer-billed AI charges over the selected window in USD cents.
	AIBilledCents int64 `json:"aiBilledCents"`
	// AIMarginCents is gross AI margin (billed minus cost) in USD cents.
	AIMarginCents int64 `json:"aiMarginCents"`
	// Orgs is total registered organizations visible in this scope.
	Orgs int64 `json:"orgs"`
	// Users is total real user accounts visible in this scope.
	Users int64 `json:"users"`
	// ActiveUsers is distinct active users in this scope over the selected window.
	ActiveUsers int64 `json:"activeUsers"`
	// O11yRequests is total observed HTTP/RPC requests over the selected window.
	O11yRequests int64 `json:"o11yRequests"`
	// O11yErrors is total observed 5xx/error spans over the selected window.
	O11yErrors int64 `json:"o11yErrors"`
	// O11yP95Ms is the 95th-percentile service latency in milliseconds.
	O11yP95Ms float64 `json:"o11yP95Ms"`
	// ProductEvents is total product telemetry events ingested over the selected window.
	ProductEvents int64 `json:"productEvents"`
	// ProductVisitors is distinct product visitors observed over the selected window.
	ProductVisitors int64 `json:"productVisitors"`
}

// AnalyticsOverviewData is the payload returned by GET /v1/analytics/overview.
type AnalyticsOverviewData struct {
	// Scope is the enforced tenant scope ("global" or "org:<slug>").
	Scope string `json:"scope"`
	// Org is the target organization slug when org-scoped, empty when global.
	Org string `json:"org,omitempty"`
	// Window is the normalized lookback window for movement KPIs.
	Window string `json:"window"`
	// KPIs holds the constant-cost executive KPI figures.
	KPIs OverviewKPIs `json:"kpis"`
	// Freshness reports snapshot timestamp, lag, staleness, source, and accuracy.
	Freshness Freshness `json:"freshness"`
}

// AnalyticsOverviewOut is the GET /v1/analytics/overview response envelope.
type AnalyticsOverviewOut struct {
	// Status is "ok" on success or "error" on failure.
	Status string `json:"status"`
	// Msg carries an error message when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data holds the executive overview KPIs and freshness metadata.
	Data *AnalyticsOverviewData `json:"data,omitempty"`
}

// overview returns constant-cost global (SuperAdmin) or org-scoped executive KPIs
// directly from analytics.global_overview / analytics.org_overview with zero per-org RPCs,
// accelerated by Hanzo KV and degrading gracefully to the last-known KV snapshot if Datastore is unavailable.
//
// Example: {"window":"30d"}
// Response: {"status":"ok","msg":"","data":{"scope":"global","window":"30d","kpis":{"revenueCents":1250000,"mrrCents":420000,"arrCents":5040000,"cashHeldCents":900000,"creditOutstandingCents":250000,"spendCents":800000,"aiRequests":150000,"aiTokens":42000000,"aiCostCents":310000,"aiBilledCents":520000,"aiMarginCents":210000,"orgs":320,"users":4800,"activeUsers":1250,"o11yRequests":9800000,"o11yErrors":1400,"o11yP95Ms":48.2,"productEvents":3100000,"productVisitors":28000},"freshness":{"asOf":"2026-11-25T00:00:00Z","lagMs":8,"stale":false,"source":"analytics.global_overview","accuracy":"exact"}}}
func (s *Service) overview(ctx context.Context, in *OverviewIn) (*AnalyticsOverviewOut, error) {
	baseScope, err := ResolveScope(ctx)
	if err != nil {
		return nil, err
	}
	reqOrg, reqWin := "", ""
	if in != nil {
		reqOrg = in.Org
		reqWin = in.Window
	}
	effScope, err := EnforceOrgParam(baseScope, reqOrg)
	if err != nil {
		return nil, PlanErrorToZip(err)
	}
	if strings.TrimSpace(reqWin) == "" {
		reqWin = "30d"
	}
	_, normWin, err := ParseWindow(reqWin)
	if err != nil {
		return nil, PlanErrorToZip(err)
	}

	ov, err := s.fetchOverview(ctx, effScope, normWin)
	if err != nil {
		return nil, PlanErrorToZip(err)
	}
	return &AnalyticsOverviewOut{Status: statusOK, Data: &ov}, nil
}

// fetchOverview reads constant-cost overview KPIs via O(1) point lookups against the
// L3 materialized serving snapshots (`analytics.serving_global_overview FINAL` /
// `analytics.serving_org_overview FINAL`), transparently falling back to
// `analytics.global_overview` / `analytics.org_overview` on cold boot before the first
// snapshot refresh tick (0 per-org RPCs). Shared by GET /v1/analytics/overview and the
// coalesced SSE Hub.
func (s *Service) fetchOverview(ctx context.Context, scope Scope, window string) (AnalyticsOverviewData, error) {
	cacheKey := CacheKey(scope.Key(), "overview:"+window)

	type cachedOverview struct {
		KPIs   OverviewKPIs `json:"kpis"`
		Source string       `json:"source"`
	}

	res, meta, err := GetOrFetch(ctx, s.cache, cacheKey, func(fctx context.Context) (cachedOverview, error) {
		var (
			rows        []map[string]any
			qerr        error
			sourceTable string
		)
		if scope.IsGlobal() {
			sourceTable = "analytics.serving_global_overview"
			rows, qerr = s.backend.Query(fctx,
				`SELECT
					orgs_total, customer_orgs, members_total, orgs_with_books,
					cash_minor, credit_minor, owed_minor, active_subscriptions, mrr_minor,
					ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d,
					ai_margin_nano_30d, ai_cache_saved_nano_30d, spans_24h, errors_24h
				FROM analytics.serving_global_overview
				WHERE scope = 'global'
				ORDER BY refreshed_at DESC
				LIMIT 1
				SETTINGS max_threads = 1, optimize_read_in_order = 1`)
			if qerr != nil && (errors.Is(qerr, ErrDatastoreUnavailable) || errors.Is(qerr, ErrOverloaded)) {
				return cachedOverview{}, qerr
			}
			if qerr != nil || len(rows) == 0 {
				sourceTable = "analytics.global_overview"
				rows, qerr = s.backend.Query(fctx,
					`SELECT
						orgs_total, customer_orgs, members_total, orgs_with_books,
						cash_minor, credit_minor, owed_minor, active_subscriptions, mrr_minor,
						ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d,
						ai_margin_nano_30d, ai_cache_saved_nano_30d, spans_24h, errors_24h
					FROM analytics.global_overview
					LIMIT 1`)
			}
		} else {
			sourceTable = "analytics.serving_org_overview"
			rows, qerr = s.backend.Query(fctx,
				`SELECT
					org, members, cash_minor, credit_minor, owed_minor, mrr_minor,
					ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d
				FROM analytics.serving_org_overview
				WHERE org = ?
				ORDER BY refreshed_at DESC
				LIMIT 1
				SETTINGS max_threads = 1`, scope.Org)
			if qerr != nil && (errors.Is(qerr, ErrDatastoreUnavailable) || errors.Is(qerr, ErrOverloaded)) {
				return cachedOverview{}, qerr
			}
			if qerr != nil || len(rows) == 0 {
				sourceTable = "analytics.org_overview"
				rows, qerr = s.backend.Query(fctx,
					`SELECT
						org, members, cash_minor, credit_minor, owed_minor, mrr_minor,
						ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d
					FROM analytics.org_overview
					WHERE org = ?
					LIMIT 1`, scope.Org)
			}
		}
		if qerr != nil {
			return cachedOverview{}, qerr
		}
		if len(rows) == 0 {
			return cachedOverview{Source: sourceTable}, nil
		}
		r := rows[0]
		nanoToCents := func(n int64) int64 {
			if n >= 0 {
				return (n + 5_000_000) / 10_000_000
			}
			return -((-n + 5_000_000) / 10_000_000)
		}
		mrr := rowInt64(r["mrr_minor"])
		aiCost := nanoToCents(rowInt64(r["ai_cost_nano_30d"]))
		aiBilled := nanoToCents(rowInt64(r["ai_billed_nano_30d"]))
		aiMargin := nanoToCents(rowInt64(r["ai_margin_nano_30d"]))
		if aiMargin == 0 && (aiBilled != 0 || aiCost != 0) {
			aiMargin = aiBilled - aiCost
		}
		orgs := rowInt64(r["orgs_total"])
		users := rowInt64(r["members_total"])
		if !scope.IsGlobal() {
			orgs = 1
			users = rowInt64(r["members"])
		}
		return cachedOverview{
			Source: sourceTable,
			KPIs: OverviewKPIs{
				MRRCents:               mrr,
				ARRCents:               mrr * 12,
				CashHeldCents:          rowInt64(r["cash_minor"]),
				CreditOutstandingCents: rowInt64(r["credit_minor"]),
				AIRequests:             rowInt64(r["ai_requests_30d"]),
				AITokens:               rowInt64(r["ai_tokens_30d"]),
				AICostCents:            aiCost,
				AIBilledCents:          aiBilled,
				AIMarginCents:          aiMargin,
				Orgs:                   orgs,
				Users:                  users,
				O11yRequests:           rowInt64(r["spans_24h"]),
				O11yErrors:             rowInt64(r["errors_24h"]),
			},
		}, nil
	})
	if err != nil {
		return AnalyticsOverviewData{}, err
	}

	source := res.Source
	if source == "" {
		if scope.IsGlobal() {
			source = "analytics.serving_global_overview"
		} else {
			source = "analytics.serving_org_overview"
		}
	}

	lagMs := max(int64(0), s.now().Sub(meta.AsOf).Milliseconds())
	return AnalyticsOverviewData{
		Scope:  scope.Key(),
		Org:    scope.Org,
		Window: window,
		KPIs:   res.KPIs,
		Freshness: Freshness{
			AsOf:     meta.AsOf.UTC().Format(time.RFC3339),
			LagMs:    lagMs,
			Stale:    meta.Stale,
			Source:   source,
			Accuracy: AccuracyExact,
		},
	}, nil
}

// ── GET /v1/analytics/health ─────────────────────────────────────────────────

// None is an empty input for parameterless analytics endpoints.
type None struct{}

// DatastoreHealth summarizes Hanzo Datastore reachability and ClickHouse merge pressure.
type DatastoreHealth struct {
	// Reachable is true when Datastore answered a live probe.
	Reachable bool `json:"reachable"`
	// ActiveParts is the total number of active MergeTree parts across the analytics database.
	ActiveParts int64 `json:"activeParts"`
	// MaxPartsPerPartition is the highest part count on any single partition (merge pressure indicator).
	MaxPartsPerPartition int64 `json:"maxPartsPerPartition"`
	// MergePressure classifies merge load: "low", "elevated", "high", or "unknown".
	MergePressure string `json:"mergePressure"`
	// Error records the probe error when Reachable is false.
	Error string `json:"error,omitempty"`
}

// HealthData is the payload returned by GET /v1/analytics/health.
type HealthData struct {
	// Status is the overall analytics plane verdict ("healthy" or "degraded").
	Status string `json:"status"`
	// Datastore reports Datastore reachability, active parts, and merge pressure.
	Datastore DatastoreHealth `json:"datastore"`
	// KV reports Hanzo KV configuration and reachability.
	KV KVStatus `json:"kv"`
	// Semaphore reports bounded Datastore query concurrency utilization.
	Semaphore SemaphoreStatus `json:"semaphore"`
	// Streams reports live coalesced SSE subscriber and scope hub counts.
	Streams StreamStatus `json:"streams"`
	// CheckedAt is the RFC3339 UTC timestamp when health was probed.
	CheckedAt string `json:"checkedAt"`
}

// HealthOut is the GET /v1/analytics/health response envelope.
type HealthOut struct {
	// Status is "ok" on success or "error" on failure.
	Status string `json:"status"`
	// Msg carries an error message when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data holds the analytics subsystem health breakdown.
	Data *HealthData `json:"data,omitempty"`
}

// health reports Datastore reachability, active MergeTree parts and merge pressure,
// Hanzo KV cache status, bounded query semaphore utilization, and active SSE stream counts.
//
// Response: {"status":"ok","msg":"","data":{"status":"healthy","datastore":{"reachable":true,"activeParts":48,"maxPartsPerPartition":12,"mergePressure":"low"},"kv":{"configured":true,"reachable":true},"semaphore":{"inFlight":1,"capacity":32,"utilizationPct":3.13,"rejected":0},"streams":{"activeStreams":14,"activeScopes":3},"checkedAt":"2026-11-25T00:00:00Z"}}
func (s *Service) health(ctx context.Context, _ *None) (*HealthOut, error) {
	if _, err := ResolveScope(ctx); err != nil {
		return nil, err
	}

	dsHealth := DatastoreHealth{
		Reachable:     false,
		MergePressure: "unknown",
	}
	if s.backend.Ready() {
		rows, err := s.backend.Query(ctx,
			`SELECT
				count() AS active_parts,
				ifNull(max(part_cnt), 0) AS max_parts_per_partition
			FROM (
				SELECT partition_id, count() AS part_cnt
				FROM system.parts
				WHERE database = 'analytics' AND active = 1
				GROUP BY table, partition_id
			)`)
		if err != nil {
			dsHealth.Error = err.Error()
		} else {
			dsHealth.Reachable = true
			if len(rows) > 0 {
				dsHealth.ActiveParts = rowInt64(rows[0]["active_parts"])
				dsHealth.MaxPartsPerPartition = rowInt64(rows[0]["max_parts_per_partition"])
			}
			switch {
			case dsHealth.MaxPartsPerPartition >= 150:
				dsHealth.MergePressure = "high"
			case dsHealth.MaxPartsPerPartition >= 50:
				dsHealth.MergePressure = "elevated"
			default:
				dsHealth.MergePressure = "low"
			}
		}
	} else {
		dsHealth.Error = ErrDatastoreUnavailable.Error()
	}

	kvStat := s.cache.Status(ctx)
	semStat := s.backend.SemaphoreStats()
	streamStat := s.hub.Stats()

	overall := "healthy"
	if !dsHealth.Reachable || dsHealth.MergePressure == "high" || (kvStat.Configured && !kvStat.Reachable) {
		overall = "degraded"
	}

	return &HealthOut{
		Status: statusOK,
		Data: &HealthData{
			Status:    overall,
			Datastore: dsHealth,
			KV:        kvStat,
			Semaphore: semStat,
			Streams:   streamStat,
			CheckedAt: s.now().UTC().Format(time.RFC3339),
		},
	}, nil
}

// ── GET /v1/analytics/freshness ──────────────────────────────────────────────

// PipelineWatermark reports ingestion watermark, lag, volume, and reconciliation drift for one pipeline.
type PipelineWatermark struct {
	// Pipeline is the canonical ingestion pipeline name (e.g. "commerce", "ai_ledger", "iam", "o11y").
	Pipeline string `json:"pipeline"`
	// WatermarkAt is the latest committed event timestamp processed by this pipeline (RFC3339 UTC).
	WatermarkAt string `json:"watermarkAt"`
	// LagMs is the end-to-end watermark lag in milliseconds.
	LagMs int64 `json:"lagMs"`
	// RowsProcessed is the cumulative row count processed by this pipeline.
	RowsProcessed int64 `json:"rowsProcessed"`
	// ReconciliationDriftCents is the financial reconciliation drift versus authoritative commerce books in USD cents.
	ReconciliationDriftCents int64 `json:"reconciliationDriftCents"`
	// Status reports pipeline health ("fresh", "lagging", or "stalled").
	Status string `json:"status"`
	// UpdatedAt is when this watermark row was last recorded (RFC3339 UTC).
	UpdatedAt string `json:"updatedAt"`
}

// FreshnessData is the payload returned by GET /v1/analytics/freshness.
type FreshnessData struct {
	// Pipelines lists per-pipeline watermarks read from analytics.watermarks.
	Pipelines []PipelineWatermark `json:"pipelines"`
	// MaxLagMs is the highest watermark lag across all pipelines in milliseconds.
	MaxLagMs int64 `json:"maxLagMs"`
	// StalePipelines counts pipelines whose status is "lagging" or "stalled".
	StalePipelines int `json:"stalePipelines"`
	// TotalDriftCents is the sum of absolute financial reconciliation drift across pipelines in USD cents.
	TotalDriftCents int64 `json:"totalDriftCents"`
	// Freshness reports snapshot timestamp, lag, staleness, source, and accuracy.
	Freshness Freshness `json:"freshness"`
}

// FreshnessOut is the GET /v1/analytics/freshness response envelope.
type FreshnessOut struct {
	// Status is "ok" on success or "error" on failure.
	Status string `json:"status"`
	// Msg carries an error message when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data holds the per-pipeline freshness watermarks and reconciliation summary.
	Data *FreshnessData `json:"data,omitempty"`
}

// freshness reports per-pipeline watermarks, lag_ms, rows processed, and financial
// reconciliation drift from analytics.watermarks, accelerated by Hanzo KV with stale-on-outage fallback.
//
// Response: {"status":"ok","msg":"","data":{"pipelines":[{"pipeline":"commerce","watermarkAt":"2026-11-25T00:00:00Z","lagMs":1200,"rowsProcessed":54000,"reconciliationDriftCents":0,"status":"fresh","updatedAt":"2026-11-25T00:00:01Z"}],"maxLagMs":1200,"stalePipelines":0,"totalDriftCents":0,"freshness":{"asOf":"2026-11-25T00:00:01Z","lagMs":10,"stale":false,"source":"analytics.watermarks","accuracy":"exact"}}}
func (s *Service) freshness(ctx context.Context, _ *None) (*FreshnessOut, error) {
	if _, err := ResolveScope(ctx); err != nil {
		return nil, err
	}

	cacheKey := CacheKey("global", "freshness:watermarks")
	pipelines, meta, err := GetOrFetch(ctx, s.cache, cacheKey, func(fctx context.Context) ([]PipelineWatermark, error) {
		rows, qerr := s.backend.Query(fctx,
			`SELECT
				pipeline,
				max(watermark_ts) AS watermark_ts,
				argMax(lag_ms, updated_at) AS lag_ms,
				argMax(rows_processed, updated_at) AS rows_processed,
				argMax(reconcile_drift_minor, updated_at) AS reconcile_drift_minor,
				argMax(status, updated_at) AS status,
				max(updated_at) AS updated_at
			FROM analytics.watermarks
			GROUP BY pipeline
			ORDER BY pipeline ASC`)
		if qerr != nil {
			return nil, qerr
		}
		out := make([]PipelineWatermark, 0, len(rows))
		for _, r := range rows {
			name := strings.TrimSpace(rowStr(r["pipeline"]))
			if name == "" {
				continue
			}
			wmAt := rowTime(r["watermark_ts"])
			updAt := rowTime(r["updated_at"])
			lag := rowInt64(r["lag_ms"])
			drift := rowInt64(r["reconcile_drift_minor"])
			st := strings.TrimSpace(rowStr(r["status"]))
			if st == "" {
				switch {
				case lag > 300_000:
					st = "stalled"
				case lag > 60_000:
					st = "lagging"
				default:
					st = "fresh"
				}
			}
			wmStr := ""
			if !wmAt.IsZero() {
				wmStr = wmAt.Format(time.RFC3339)
			}
			updStr := ""
			if !updAt.IsZero() {
				updStr = updAt.Format(time.RFC3339)
			}
			out = append(out, PipelineWatermark{
				Pipeline:                 name,
				WatermarkAt:              wmStr,
				LagMs:                    lag,
				RowsProcessed:            rowInt64(r["rows_processed"]),
				ReconciliationDriftCents: drift,
				Status:                   st,
				UpdatedAt:                updStr,
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, PlanErrorToZip(err)
	}

	var maxLag, totalDrift int64
	staleCount := 0
	for _, p := range pipelines {
		if p.LagMs > maxLag {
			maxLag = p.LagMs
		}
		if p.Status == "lagging" || p.Status == "stalled" {
			staleCount++
		}
		if p.ReconciliationDriftCents < 0 {
			totalDrift += -p.ReconciliationDriftCents
		} else {
			totalDrift += p.ReconciliationDriftCents
		}
	}

	lagMs := max(int64(0), s.now().Sub(meta.AsOf).Milliseconds())
	return &FreshnessOut{
		Status: statusOK,
		Data: &FreshnessData{
			Pipelines:       pipelines,
			MaxLagMs:        maxLag,
			StalePipelines:  staleCount,
			TotalDriftCents: totalDrift,
			Freshness: Freshness{
				AsOf:     meta.AsOf.UTC().Format(time.RFC3339),
				LagMs:    lagMs,
				Stale:    meta.Stale,
				Source:   "analytics.watermarks",
				Accuracy: AccuracyExact,
			},
		},
	}, nil
}
