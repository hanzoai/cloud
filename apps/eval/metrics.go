package eval

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// metrics.go serves the AI OBSERVABILITY DASHBOARD aggregation behind
// GET /v1/evals/metrics — the native, per-org / per-project "AI overview": which
// models are used, request volume, cost, tokens (prompt / completion / total),
// error & success rate, and latency percentiles (p50 / p95 / p99) over a window.
// It is the Langfuse home-dashboard, native — the MIT datastore query shapes
// (counts / cost / tokens BY MODEL over time, error rate, latency quantiles)
// ported to pure Go over our datastore; none of Langfuse's commercial (ee/) code
// is used.
//
// TWO datastore sources, ONE shared client (clients/datastore), both READ-ONLY:
//   - hanzo.cloud_usage — the proven spend/usage ledger (ai/object-owned; the SAME
//     table ListObservations reads). Every production generation lands here, so it
//     is the AUTHORITATIVE source for counts, tokens, cost, errors, model & user
//     cardinality. count()/sum()/uniqExact() GROUP BY model / time bucket.
//   - event.span GenAI spans — the OTel gen_ai.* spans ai/object emits carry
//     duration (ns) + gen_ai.request.model + gen_ai.hanzo.org_id, so per-model and
//     overall latency percentiles come from here. This read is BEST-EFFORT: if the
//     span store is unreachable or holds no GenAI spans, latency is honestly absent
//     (Latency.Available=false, nil percentiles) while every ledger metric still
//     renders — a latency miss never fails the board (the ledger is the core).
//
// TENANT ISOLATION: org is the validated tenant (principal.Org), bound as a
// positional datastore parameter (never interpolated), the FIRST predicate on
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
//
// Project is the org SUB-SCOPE (server-minted, "" for the org's default project ==
// whole-org view). When non-empty it ANDs a project predicate on the ledger and
// the latency spans, so the board narrows to that project WITHIN the org — org
// stays the hard tenant boundary regardless.
type MetricsFilter struct {
	Org      string
	Project  string
	AllOrgs  bool
	Since    time.Time
	Until    time.Time
	Interval string
	TopN     int
}

// ── wire shapes ───────────────────────────────────────────────────────────────

type BoardScope struct {
	Org     string `json:"org"`     // the org the board covers; "" when it covers all of them
	Project string `json:"project"` // the sub-scope within the org; "" is the whole org
	AllOrgs bool   `json:"allOrgs"` // true when a platform admin is seeing every org at once
}

type BoardRange struct {
	Range    string `json:"range"`    // echoed label (24h | 7d | 30d | custom)
	Start    string `json:"start"`    // RFC3339 (UTC)
	End      string `json:"end"`      // RFC3339 (UTC)
	Interval string `json:"interval"` // hour | day
}

type BoardTotals struct {
	Generations      int64   `json:"generations"`      // how many model calls the window holds
	PromptTokens     int64   `json:"promptTokens"`     // tokens sent to the models
	CompletionTokens int64   `json:"completionTokens"` // tokens the models answered with
	TotalTokens      int64   `json:"totalTokens"`      // prompt plus completion
	CostCents        int64   `json:"costCents"`        // what the window cost, in cents
	Errors           int64   `json:"errors"`           // calls that did not succeed
	SuccessRate      float64 `json:"successRate"`      // share of calls that succeeded, 0..1
	Models           int64   `json:"models"`           // how many distinct models were called
	Users            int64   `json:"users"`            // how many distinct users called them
}

// BoardPoint is one gap-filled time bucket for the volume/cost/token/error series.
type BoardPoint struct {
	T           string `json:"t"`           // RFC3339 (UTC) bucket start
	Generations int64  `json:"generations"` // model calls in this bucket
	CostCents   int64  `json:"costCents"`   // what this bucket cost, in cents
	TotalTokens int64  `json:"totalTokens"` // tokens in this bucket
	Errors      int64  `json:"errors"`      // calls in this bucket that did not succeed
}

// ModelStat is one row of the model-usage table (or the folded "other" bucket).
// The latency percentiles are pointers so "no latency data for this model" is a
// null the console renders as "—", never a fabricated 0.
type ModelStat struct {
	Model            string   `json:"model"`                // the model this row is about, or "other" for the fold
	Provider         string   `json:"provider"`             // who serves it
	Requests         int64    `json:"requests"`             // calls to this model in the window
	PromptTokens     int64    `json:"promptTokens"`         // tokens sent to it
	CompletionTokens int64    `json:"completionTokens"`     // tokens it answered with
	TotalTokens      int64    `json:"totalTokens"`          // prompt plus completion
	CostCents        int64    `json:"costCents"`            // what this model cost, in cents
	Errors           int64    `json:"errors"`               // calls to it that did not succeed
	ErrorRate        float64  `json:"errorRate"`            // share of its calls that failed, 0..1
	CostPct          float64  `json:"costPct"`              // share of total spend, 0..100
	P50Ms            *float64 `json:"p50Ms"`                // median latency, null when no spans carry it
	P95Ms            *float64 `json:"p95Ms"`                // 95th-percentile latency, null when unknown
	P99Ms            *float64 `json:"p99Ms"`                // 99th-percentile latency, null when unknown
	ModelCount       int      `json:"modelCount,omitempty"` // >0 only on the "other" fold
}

// LatencyStat is the board's overall latency. Available=false with nil percentiles
// is the honest "no GenAI spans" state.
type LatencyStat struct {
	Available bool     `json:"available"` // false when no GenAI spans carry timing; the percentiles are then null
	P50Ms     *float64 `json:"p50Ms"`     // median latency over the window
	P95Ms     *float64 `json:"p95Ms"`     // 95th-percentile latency
	P99Ms     *float64 `json:"p99Ms"`     // 99th-percentile latency
}

// Board is the full AI-overview dashboard payload.
type Board struct {
	Scope   BoardScope   `json:"scope"`           // whose numbers these are
	Range   BoardRange   `json:"range"`           // the window they were computed over, echoed back
	Totals  BoardTotals  `json:"totals"`          // the window's headline numbers
	Series  []BoardPoint `json:"series"`          // one gap-filled bucket per interval, so a chart never breaks
	ByModel []ModelStat  `json:"byModel"`         // the top models by spend
	Other   *ModelStat   `json:"other,omitempty"` // the long tail beyond the top models, folded into one row
	Latency LatencyStat  `json:"latency"`         // overall latency percentiles from the GenAI spans
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

// boardQuery is the window and bucket a board is computed over.
type boardQuery struct {
	// Range is 24h (the default), 7d or 30d. Anything else normalises to 24h
	// rather than failing, so the board always has a valid window.
	Range string `json:"range"`
	// Interval overrides the bucket the series is grouped into: "hour" or "day".
	// Any other value leaves the range's own default in place.
	Interval string `json:"interval"`
}

// metricsBoard is your org's AI overview board over a window: totals
// (generations, prompt and completion tokens, cost in cents, errors, success
// rate, distinct models and users), a gap-filled time series, a per-model
// breakdown with the long tail folded into "other", and latency percentiles read
// from the GenAI spans.
//
// The window the answer was actually computed over is echoed back, so a client
// never has to infer it. A platform admin sees the board across ALL orgs;
// everyone else sees their own.
//
// The board is HONEST-EMPTY where it cannot be computed: with no datastore wired,
// or under a named project scope the usage ledger does not yet carry, it answers a
// valid board with zero totals and a flat series rather than a fabricated number
// or a 500. Requires a validated principal; 403 without one.
func (s *service) metricsBoard(ctx context.Context, in *boardQuery) (*Board, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}

	label, since, until, interval := resolveRange(strings.TrimSpace(in.Range))
	if iv := strings.TrimSpace(in.Interval); iv == "hour" || iv == "day" {
		interval = iv
	}
	// Project is the server-minted sub-scope ("" for the org's default project ==
	// whole-org board). Same resolver as the trace path so the two never drift.
	project := scope(ctx)

	f := MetricsFilter{
		Org:      org, // authoritative — never a client header
		Project:  project,
		AllOrgs:  admin(ctx),
		Since:    since,
		Until:    until,
		Interval: interval,
		TopN:     defaultBoardTopN,
	}

	board := emptyBoard(f)
	// The default-project (whole-org) board queries the ledger today. A NAMED
	// project additionally needs the cloud_usage `project` column that the ai write
	// path populates; until this cloud pins that ai version, a named-project board
	// is honest-empty (never a fabricated number, never a 500 on a column that does
	// not yet exist). Activation is one edit — drop the `f.Project == ""` guard —
	// once the ledger carries project; the query plumbing (usageWhere) is already
	// project-aware and tested.
	if s.tel != nil && f.Project == "" {
		b, err := s.tel.Metrics(ctx, f)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
		}
		board = b
	}
	board.Range.Range = label
	board.Scope.Project = project
	return &board, nil
}

// admin reports that the caller is a platform SuperAdmin (c.IsAdmin() — the
// X-User-IsAdmin the identity boundary mints only for a validated owner ==
// AdminOrg). It needs the REQUEST rather than the tenant because admin-ness lives
// in a header principal.OrgFrom does not carry. False off the HTTP path: no
// request, no attested caller, no admin rights.
func admin(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && c.IsAdmin()
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

// Metrics runs the aggregate queries against the datastore ledger (+ best-effort
// GenAI-span latency) and assembles the board. A ledger error is surfaced (the
// board is the core signal); a latency-span error degrades to Available=false.
func (t *dsTelemetry) Metrics(ctx context.Context, f MetricsFilter) (Board, error) {
	if f.Org == "" {
		return Board{}, fmt.Errorf("evals telemetry: metrics read missing org")
	}
	if !datastore.Ready() {
		return Board{}, fmt.Errorf("evals telemetry: datastore not connected")
	}
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
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
	totalsRows, err := datastore.Query(ctx, totalsSQL, args...)
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
	seriesRows, err := datastore.Query(ctx, seriesSQL, append([]any{stepSec}, args...)...)
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
	modelRows, err := datastore.Query(ctx, modelSQL, args...)
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

// latency reads per-model + overall latency percentiles from the GenAI spans on
// the plane (event.span — a gen_ai span IS the observation of record, HIP-0132).
// Best-effort: an error (span store unreachable, table absent) returns an empty
// per-model map and an unavailable overall — never an error — so a latency miss
// cannot fail the dashboard. The org gate mirrors the ledger: non-admin is pinned
// to gen_ai.hanzo.org_id; a SuperAdmin sees every org.
func (t *dsTelemetry) latency(ctx context.Context, f MetricsFilter) (map[string]latPercentiles, LatencyStat) {
	const spanTable = "event.span"
	where := "attributes['gen_ai.system'] = 'hanzo' AND time >= ? AND time < ?"
	args := []any{chTime(f.Since), chTime(f.Until)}
	if !f.AllOrgs {
		where += " AND attributes['gen_ai.hanzo.org_id'] = ?"
		args = append(args, f.Org)
	}
	if f.Project != "" {
		// The ai emit path tags gen_ai.hanzo.project on the span; narrowing here
		// keeps per-project latency consistent with the per-project ledger board.
		where += " AND attributes['gen_ai.hanzo.project'] = ?"
		args = append(args, f.Project)
	}

	perModel := map[string]latPercentiles{}
	modelSQL := "SELECT attributes['gen_ai.request.model'] AS model, " +
		"quantile(0.5)(duration) AS p50, quantile(0.95)(duration) AS p95, " +
		"quantile(0.99)(duration) AS p99, count() AS n " +
		"FROM " + spanTable + " WHERE " + where + " GROUP BY model"
	if rows, err := datastore.Query(ctx, modelSQL, args...); err == nil {
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
	overallSQL := "SELECT quantile(0.5)(duration) AS p50, quantile(0.95)(duration) AS p95, " +
		"quantile(0.99)(duration) AS p99, count() AS n " +
		"FROM " + spanTable + " WHERE " + where
	if rows, err := datastore.Query(ctx, overallSQL, args...); err == nil {
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

// usageWhere builds the cloud_usage time (+ org + project) predicate. Times are
// bound as datastore DateTime string literals (the proven ai/object cloud_usage
// pattern); the org and project are bound as positional parameters (never
// interpolated). A SuperAdmin (AllOrgs) drops the org predicate for the
// platform-wide board; a non-empty Project ANDs the project predicate.
//
// The project predicate reads the cloud_usage `project` column, which the ai
// write path (usageRecord ← X-Project-Id) and DDL populate. Until an org's rows
// carry a project, a named-project board is honest-empty; the default-project /
// whole-org board (Project == "") is unaffected.
func usageWhere(f MetricsFilter) (string, []any) {
	where := "timestamp >= ? AND timestamp < ?"
	args := []any{chTime(f.Since), chTime(f.Until)}
	if !f.AllOrgs {
		where += " AND organization = ?"
		args = append(args, f.Org)
	}
	if f.Project != "" {
		where += " AND project = ?"
		args = append(args, f.Project)
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

// ── pure assemblers (unit-tested without datastore) ──────────────────────────

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

// assembleSeries turns sparse datastore buckets into an evenly-spaced, gap-filled
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
// a bucket width, shared by the datastore GROUP BY and the Go gap-fill so the two
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
