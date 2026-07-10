package eval

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// metrics.go serves the AI OBSERVABILITY DASHBOARD aggregation behind
// GET /v1/evals/metrics — the native, per-org / per-project "AI overview": which
// models are used, request volume, cost, tokens (prompt / completion / total),
// error & success rate, and latency percentiles (p50 / p95 / p99) over a window.
// It is the Langfuse home-dashboard, native — the MIT ClickHouse query shapes
// (counts / cost / tokens BY MODEL over time, error rate, latency quantiles)
// ported to pure Go over our datastore; none of Langfuse's commercial (ee/) code
// is used.
//
// TWO datastore sources, ONE shared client (aiobject peer), both READ-ONLY:
//   - hanzo.cloud_usage — the proven spend/usage ledger (ai/object-owned; the SAME
//     table ListObservations reads). Every production generation lands here, so it
//     is the AUTHORITATIVE source for counts, tokens, cost, errors, model & user
//     cardinality. count()/sum()/uniqExact() GROUP BY model / time bucket.
//   - signoz_traces GenAI spans — the OTel gen_ai.* spans ai/object emits carry
//     duration_nano + gen_ai.request.model + gen_ai.hanzo.org_id, so per-model and
//     overall latency percentiles come from here. This read is BEST-EFFORT: if the
//     span store is unreachable or holds no GenAI spans, latency is honestly absent
//     (Latency.Available=false, nil percentiles) while every ledger metric still
//     renders — a latency miss never fails the board (the ledger is the core).
//
// TENANT ISOLATION: org is the validated tenant (principal.Tenant), bound as a
// positional ClickHouse parameter (never interpolated), the FIRST predicate on
// every query. A validated platform SuperAdmin (c.IsAdmin()) runs AllOrgs — the
// board then aggregates every org with no org predicate — mirroring the o11y RED
// view exactly; a non-admin can only ever see its own org.
//
// SCOPING GAPS (reported, not faked):
//   - PROJECT: hanzo.cloud_usage has no project column and the ai/object write path
//     (zapWriteUsage / usageRecord) carries no project, so per-project attribution
//     does not exist in the ledger yet. The handler threads a project so the surface
//     is complete the moment the column lands: the default/empty project == the org's
//     whole ledger (principal.IsDefaultProject); a non-default project is honest-empty
//     until attribution exists. To make it real: add `project` to usageRecord (fed
//     from the gateway X-Project-Id) + the cloud_usage DDL + this WHERE.
//   - LATENCY: the ledger has no duration column, so latency is sourced from the
//     GenAI spans above rather than cloud_usage.

// MetricsFilter is the resolved, already-authorized dashboard query. Org is the
// validated tenant; AllOrgs (SuperAdmin) drops the org predicate; the window
// [Since, Until) is closed; Interval is the server-chosen bucket ("hour" | "day");
// TopN bounds the by-model table (the rest fold into "other").
type MetricsFilter struct {
	Org      string
	AllOrgs  bool
	Since    time.Time
	Until    time.Time
	Interval string
	TopN     int
}

// ── wire shapes ───────────────────────────────────────────────────────────────

type BoardScope struct {
	Org     string `json:"org"` // "" when AllOrgs
	Project string `json:"project"`
	AllOrgs bool   `json:"allOrgs"`
}

type BoardRange struct {
	Range    string `json:"range"`    // echoed label (24h | 7d | 30d | custom)
	Start    string `json:"start"`    // RFC3339 (UTC)
	End      string `json:"end"`      // RFC3339 (UTC)
	Interval string `json:"interval"` // hour | day
}

type BoardTotals struct {
	Generations      int64   `json:"generations"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	CostCents        int64   `json:"costCents"`
	Errors           int64   `json:"errors"`
	SuccessRate      float64 `json:"successRate"` // 0..1
	Models           int64   `json:"models"`
	Users            int64   `json:"users"`
}

// BoardPoint is one gap-filled time bucket for the volume/cost/token/error series.
type BoardPoint struct {
	T           string `json:"t"` // RFC3339 (UTC) bucket start
	Generations int64  `json:"generations"`
	CostCents   int64  `json:"costCents"`
	TotalTokens int64  `json:"totalTokens"`
	Errors      int64  `json:"errors"`
}

// ModelStat is one row of the model-usage table (or the folded "other" bucket).
// The latency percentiles are pointers so "no latency data for this model" is a
// null the console renders as "—", never a fabricated 0.
type ModelStat struct {
	Model            string   `json:"model"`
	Provider         string   `json:"provider"`
	Requests         int64    `json:"requests"`
	PromptTokens     int64    `json:"promptTokens"`
	CompletionTokens int64    `json:"completionTokens"`
	TotalTokens      int64    `json:"totalTokens"`
	CostCents        int64    `json:"costCents"`
	Errors           int64    `json:"errors"`
	ErrorRate        float64  `json:"errorRate"` // 0..1
	CostPct          float64  `json:"costPct"`   // share of total spend, 0..100
	P50Ms            *float64 `json:"p50Ms"`
	P95Ms            *float64 `json:"p95Ms"`
	P99Ms            *float64 `json:"p99Ms"`
	ModelCount       int      `json:"modelCount,omitempty"` // >0 only on the "other" fold
}

// LatencyStat is the board's overall latency. Available=false with nil percentiles
// is the honest "no GenAI spans" state.
type LatencyStat struct {
	Available bool     `json:"available"`
	P50Ms     *float64 `json:"p50Ms"`
	P95Ms     *float64 `json:"p95Ms"`
	P99Ms     *float64 `json:"p99Ms"`
}

// Board is the full AI-overview dashboard payload.
type Board struct {
	Scope   BoardScope   `json:"scope"`
	Range   BoardRange   `json:"range"`
	Totals  BoardTotals  `json:"totals"`
	Series  []BoardPoint `json:"series"`
	ByModel []ModelStat  `json:"byModel"`
	Other   *ModelStat   `json:"other,omitempty"`
	Latency LatencyStat  `json:"latency"`
}

// latPercentiles is the per-model latency read out of the GenAI spans.
type latPercentiles struct {
	p50, p95, p99 *float64
}

const (
	defaultBoardTopN = 8
	maxBoardTopN     = 50
)

// emptyBoard is the honest-empty board: a valid scope + window, zero totals, a
// gap-filled all-zero series (so the chart draws a flat line, never a crash) and
// no models/latency. Used for the no-datastore path, the non-default-project gap,
// and the in-memory store.
func emptyBoard(f MetricsFilter) Board {
	return Board{
		Scope:   BoardScope{Org: scopeOrg(f.Org, f.AllOrgs), AllOrgs: f.AllOrgs},
		Range:   boardRange(f),
		Totals:  BoardTotals{},
		Series:  assembleSeries(f.Since, f.Until, boardStep(f.Interval), nil),
		ByModel: []ModelStat{},
		Latency: LatencyStat{Available: false},
	}
}

func boardRange(f MetricsFilter) BoardRange {
	return BoardRange{
		Start:    f.Since.UTC().Format(time.RFC3339),
		End:      f.Until.UTC().Format(time.RFC3339),
		Interval: f.Interval,
	}
}

func scopeOrg(org string, allOrgs bool) string {
	if allOrgs {
		return ""
	}
	return org
}

// ── HTTP handler ──────────────────────────────────────────────────────────────

// metricsBoard serves GET /v1/evals/metrics — the AI observability dashboard for
// the caller's org (a validated SuperAdmin, c.IsAdmin(), sees every org). It
// resolves the window from a fixed range preset (?range, default 24h), optionally
// overrides the bucket (?interval=hour|day), and threads ?project. Tenant isolation
// is the principal gate: no validated principal ⇒ 403. When telemetry is disabled
// (no datastore) or the project is non-default (no ledger attribution yet), the
// board is honest-empty — a valid, all-zero board, never a 503 or a fabricated
// number.
func (s *service) metricsBoard(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}

	label, since, until, interval := resolveRange(strings.TrimSpace(c.Query("range")))
	if iv := strings.TrimSpace(c.Query("interval")); iv == "hour" || iv == "day" {
		interval = iv
	}
	project := strings.TrimSpace(c.Query("project"))

	f := MetricsFilter{
		Org:      org, // authoritative — never a client header
		AllOrgs:  c.IsAdmin(),
		Since:    since,
		Until:    until,
		Interval: interval,
		TopN:     defaultBoardTopN,
	}

	board := emptyBoard(f)
	if s.tel != nil && principal.IsDefaultProject(project) {
		b, err := s.tel.Metrics(c.Context(), f)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
		}
		board = b
	}
	board.Range.Range = label
	board.Scope.Project = project
	return c.JSON(http.StatusOK, board)
}

// resolveRange maps a range preset to its echoed label, the closed window
// [since, until) and the default bucket. Unknown/empty normalizes to 24h/hour so
// the surface always has a valid window; ?interval may override the bucket.
func resolveRange(label string) (string, time.Time, time.Time, string) {
	until := time.Now().UTC()
	switch label {
	case "7d":
		return "7d", until.Add(-7 * 24 * time.Hour), until, "day"
	case "30d":
		return "30d", until.Add(-30 * 24 * time.Hour), until, "day"
	default:
		return "24h", until.Add(-24 * time.Hour), until, "hour"
	}
}

// ── datastore implementation ──────────────────────────────────────────────────

// Metrics runs the aggregate queries against the ClickHouse ledger (+ best-effort
// GenAI-span latency) and assembles the board. A ledger error is surfaced (the
// board is the core signal); a latency-span error degrades to Available=false.
func (t *dsTelemetry) Metrics(ctx context.Context, f MetricsFilter) (Board, error) {
	if f.Org == "" {
		return Board{}, fmt.Errorf("evals telemetry: metrics read missing org")
	}
	if !aiobject.DatastoreEnabled() {
		return Board{}, fmt.Errorf("evals telemetry: datastore not connected")
	}
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return Board{}, fmt.Errorf("evals telemetry: ensure cloud_usage: %w", err)
	}

	where, args := usageWhere(f)
	stepSec := boardStepSec(f.Interval)

	// Totals.
	totalsSQL := "SELECT count() AS generations, sum(prompt_tokens) AS prompt_tokens, " +
		"sum(completion_tokens) AS completion_tokens, sum(total_tokens) AS total_tokens, " +
		"sum(cost_cents) AS cost_cents, " +
		"countIf(status != '' AND status != 'success') AS errors, " +
		"uniqExact(model) AS models, uniqExact(user_id) AS users " +
		"FROM " + t.table("cloud_usage") + " WHERE " + where
	totalsRows, err := aiobject.DatastoreQuery(ctx, totalsSQL, args...)
	if err != nil {
		return Board{}, fmt.Errorf("evals metrics: totals: %w", err)
	}
	totals := assembleTotals(firstRow(totalsRows))

	// Time series (gap-filled in Go). stepSec is bound first, then the WHERE args.
	seriesSQL := "SELECT toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket, " +
		"count() AS generations, sum(cost_cents) AS cost_cents, " +
		"sum(total_tokens) AS total_tokens, " +
		"countIf(status != '' AND status != 'success') AS errors " +
		"FROM " + t.table("cloud_usage") + " WHERE " + where + " GROUP BY bucket ORDER BY bucket"
	seriesRows, err := aiobject.DatastoreQuery(ctx, seriesSQL, append([]any{stepSec}, args...)...)
	if err != nil {
		return Board{}, fmt.Errorf("evals metrics: series: %w", err)
	}

	// By model (bounded to 100 rows; the assembler folds beyond TopN into "other").
	modelSQL := "SELECT model, any(provider) AS provider, count() AS requests, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(total_tokens) AS total_tokens, sum(cost_cents) AS cost_cents, " +
		"countIf(status != '' AND status != 'success') AS errors " +
		"FROM " + t.table("cloud_usage") + " WHERE " + where +
		" GROUP BY model ORDER BY cost_cents DESC LIMIT 100"
	modelRows, err := aiobject.DatastoreQuery(ctx, modelSQL, args...)
	if err != nil {
		return Board{}, fmt.Errorf("evals metrics: by-model: %w", err)
	}

	// Latency (best-effort GenAI spans). Any failure → honest Available=false.
	byModelLat, overallLat := t.latency(ctx, f)

	byModel, other := assembleByModel(modelRows, byModelLat, f.TopN, totals.CostCents)

	return Board{
		Scope:   BoardScope{Org: scopeOrg(f.Org, f.AllOrgs), AllOrgs: f.AllOrgs},
		Range:   boardRange(f),
		Totals:  totals,
		Series:  assembleSeries(f.Since, f.Until, boardStep(f.Interval), seriesRows),
		ByModel: byModel,
		Other:   other,
		Latency: overallLat,
	}, nil
}

// latency reads per-model + overall latency percentiles from the GenAI spans in
// signoz_traces. Best-effort: an error (span store unreachable, table absent)
// returns an empty per-model map and an unavailable overall — never an error —
// so a latency miss cannot fail the dashboard. The org gate mirrors the ledger:
// non-admin is pinned to gen_ai.hanzo.org_id; a SuperAdmin sees every org.
func (t *dsTelemetry) latency(ctx context.Context, f MetricsFilter) (map[string]latPercentiles, LatencyStat) {
	const spanTable = "signoz_traces.distributed_signoz_index_v3"
	where := "attributes_string['gen_ai.system'] = 'hanzo' AND timestamp >= ? AND timestamp < ?"
	args := []any{chTime(f.Since), chTime(f.Until)}
	if !f.AllOrgs {
		where += " AND attributes_string['gen_ai.hanzo.org_id'] = ?"
		args = append(args, f.Org)
	}

	perModel := map[string]latPercentiles{}
	modelSQL := "SELECT attributes_string['gen_ai.request.model'] AS model, " +
		"quantile(0.5)(duration_nano) AS p50, quantile(0.95)(duration_nano) AS p95, " +
		"quantile(0.99)(duration_nano) AS p99, count() AS n " +
		"FROM " + spanTable + " WHERE " + where + " GROUP BY model"
	if rows, err := aiobject.DatastoreQuery(ctx, modelSQL, args...); err == nil {
		for _, r := range rows {
			model := asString(r["model"])
			if model == "" || asInt64(r["n"]) == 0 {
				continue
			}
			perModel[model] = latPercentiles{
				p50: nsToMsPtr(r["p50"]), p95: nsToMsPtr(r["p95"]), p99: nsToMsPtr(r["p99"]),
			}
		}
	} else {
		t.log.Warn("evals metrics: latency by-model unavailable (GenAI spans)", "err", err)
	}

	overall := LatencyStat{Available: false}
	overallSQL := "SELECT quantile(0.5)(duration_nano) AS p50, quantile(0.95)(duration_nano) AS p95, " +
		"quantile(0.99)(duration_nano) AS p99, count() AS n " +
		"FROM " + spanTable + " WHERE " + where
	if rows, err := aiobject.DatastoreQuery(ctx, overallSQL, args...); err == nil {
		if r := firstRow(rows); asInt64(r["n"]) > 0 {
			overall = LatencyStat{
				Available: true,
				P50Ms:     nsToMsPtr(r["p50"]), P95Ms: nsToMsPtr(r["p95"]), P99Ms: nsToMsPtr(r["p99"]),
			}
		}
	} else {
		t.log.Warn("evals metrics: overall latency unavailable (GenAI spans)", "err", err)
	}
	return perModel, overall
}

// usageWhere builds the cloud_usage time (+ org) predicate. Times are bound as
// ClickHouse DateTime string literals (the proven ai/object cloud_usage pattern);
// the org is bound as a positional parameter (never interpolated). A SuperAdmin
// (AllOrgs) drops the org predicate for the platform-wide board.
func usageWhere(f MetricsFilter) (string, []any) {
	where := "timestamp >= ? AND timestamp < ?"
	args := []any{chTime(f.Since), chTime(f.Until)}
	if !f.AllOrgs {
		where += " AND organization = ?"
		args = append(args, f.Org)
	}
	return where, args
}

// ── in-memory implementation ──────────────────────────────────────────────────

// Metrics on the in-memory store holds no production cloud_usage ledger (that
// table is ai/object-owned and datastore-only), so it returns an honest-empty
// board — but still enforces the authoritative org (tenant isolation).
func (m *memTelemetry) Metrics(_ context.Context, f MetricsFilter) (Board, error) {
	if f.Org == "" {
		return Board{}, fmt.Errorf("evals telemetry: metrics read missing org")
	}
	return emptyBoard(f), nil
}

// ── pure assemblers (unit-tested without ClickHouse) ──────────────────────────

func assembleTotals(r map[string]any) BoardTotals {
	gen := asInt64(r["generations"])
	errs := asInt64(r["errors"])
	tot := BoardTotals{
		Generations:      gen,
		PromptTokens:     asInt64(r["prompt_tokens"]),
		CompletionTokens: asInt64(r["completion_tokens"]),
		TotalTokens:      asInt64(r["total_tokens"]),
		CostCents:        asInt64(r["cost_cents"]),
		Errors:           errs,
		Models:           asInt64(r["models"]),
		Users:            asInt64(r["users"]),
	}
	if gen > 0 {
		tot.SuccessRate = round4(float64(gen-errs) / float64(gen))
	}
	return tot
}

// assembleSeries turns sparse ClickHouse buckets into an evenly-spaced, gap-filled
// series over [since, until) so the console charts a continuous line. Bucket
// alignment matches toStartOfInterval(step): Go's Truncate over the same step
// lands on the same UTC boundaries for hour/day widths.
func assembleSeries(since, until time.Time, step time.Duration, rows []map[string]any) []BoardPoint {
	type agg struct{ gen, cost, tokens, errs int64 }
	idx := make(map[int64]agg, len(rows))
	for _, r := range rows {
		bt := asTime(r["bucket"]).UTC().Truncate(step)
		idx[bt.Unix()] = agg{
			gen:    asInt64(r["generations"]),
			cost:   asInt64(r["cost_cents"]),
			tokens: asInt64(r["total_tokens"]),
			errs:   asInt64(r["errors"]),
		}
	}
	out := make([]BoardPoint, 0, 64)
	for ts := since.UTC().Truncate(step); ts.Before(until); ts = ts.Add(step) {
		a := idx[ts.Unix()]
		out = append(out, BoardPoint{
			T:           ts.Format(time.RFC3339),
			Generations: a.gen,
			CostCents:   a.cost,
			TotalTokens: a.tokens,
			Errors:      a.errs,
		})
	}
	return out
}

// assembleByModel builds the per-model table (rows already cost-sorted from SQL),
// merges the GenAI-span latency by model, computes each row's error rate + cost
// share, and folds everything beyond TopN into a single "other" bucket. Pure.
func assembleByModel(rows []map[string]any, lat map[string]latPercentiles, topN int, totalCost int64) ([]ModelStat, *ModelStat) {
	if topN <= 0 {
		topN = defaultBoardTopN
	}
	if topN > maxBoardTopN {
		topN = maxBoardTopN
	}
	stats := make([]ModelStat, 0, len(rows))
	for _, r := range rows {
		model := asString(r["model"])
		reqs := asInt64(r["requests"])
		errs := asInt64(r["errors"])
		s := ModelStat{
			Model:            model,
			Provider:         asString(r["provider"]),
			Requests:         reqs,
			PromptTokens:     asInt64(r["prompt_tokens"]),
			CompletionTokens: asInt64(r["completion_tokens"]),
			TotalTokens:      asInt64(r["total_tokens"]),
			CostCents:        asInt64(r["cost_cents"]),
			Errors:           errs,
			CostPct:          pct100(asInt64(r["cost_cents"]), totalCost),
		}
		if reqs > 0 {
			s.ErrorRate = round4(float64(errs) / float64(reqs))
		}
		if l, ok := lat[model]; ok {
			s.P50Ms, s.P95Ms, s.P99Ms = l.p50, l.p95, l.p99
		}
		stats = append(stats, s)
	}
	if len(stats) <= topN {
		return stats, nil
	}
	head := stats[:topN:topN]
	other := ModelStat{Model: "other"}
	for _, s := range stats[topN:] {
		other.Requests += s.Requests
		other.PromptTokens += s.PromptTokens
		other.CompletionTokens += s.CompletionTokens
		other.TotalTokens += s.TotalTokens
		other.CostCents += s.CostCents
		other.Errors += s.Errors
		other.ModelCount++
	}
	if other.Requests > 0 {
		other.ErrorRate = round4(float64(other.Errors) / float64(other.Requests))
	}
	other.CostPct = pct100(other.CostCents, totalCost)
	return head, &other
}

// ── helpers ───────────────────────────────────────────────────────────────────

func firstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return map[string]any{}
	}
	return rows[0]
}

// boardStepSec / boardStep are the ONE mapping from the server-chosen interval to
// a bucket width, shared by the ClickHouse GROUP BY and the Go gap-fill so the two
// can never drift.
func boardStepSec(interval string) int {
	if interval == "day" {
		return 86400
	}
	return 3600
}

func boardStep(interval string) time.Duration {
	return time.Duration(boardStepSec(interval)) * time.Second
}

func chTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// nsToMsPtr converts a nanosecond quantile to a rounded-ms pointer, or nil when the
// column is absent/zero (no data) so the wire carries null, not a fake 0.
func nsToMsPtr(v any) *float64 {
	ns := asFloat(v)
	if ns <= 0 {
		return nil
	}
	ms := round2(ns / 1e6)
	return &ms
}

func pct100(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return round2(float64(part) / float64(total) * 100)
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
func round4(f float64) float64 { return float64(int64(f*10000+0.5)) / 10000 }
