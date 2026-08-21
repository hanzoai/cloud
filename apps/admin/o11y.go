// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package admin

// o11y — GET /v1/admin/o11y, the GLOBAL fleet-wide observability read that powers
// the operator's o11y board on admin.hanzo.ai. It is the un-org-scoped twin of the
// per-org console o11y: the same signals, aggregated across EVERY tenant, over the
// ONE hanzoai/datastore (datastore) — the same warehouse + shared client
// (datastore.Query) the analytics/compute lenses already use, no second
// connection.
//
// Signals, each from its canonical table in the one datastore — the EVENT PLANE
// plus the ai ledger, no o11y_* database anywhere:
//   - LLM usage  → hanzo.cloud_usage : requests, tokens, cost, errors, top orgs, top models
//   - Traces     → event.span        : request count, latency p50/p95/p99,
//                                      error rate, top services
//   - Logs       → event.log         : fleet log volume + volume-over-time
//   - LLM gens   → event.span        : gen_ai spans — a gen_ai span IS the
//                                      observation of record (see o11yAIObs)
//
// SUPERADMIN ONLY (core.Admit, the op's first line): the gateway strips a client
// X-Org-Id and re-mints from the JWT owner, and this handler applies NO org filter,
// so it is the ONE place a fleet operator crosses tenants — a non-admin bearer is
// refused 403 before a single row is read. Fail-closed.
//
// Honest by construction, exactly like compute/analytics: no datastore connected →
// the real empty aggregate, never a fabricated fleet. admin READS only; it owns and
// creates NO table (the ZAP-fed collector + the ai-owned cloud_usage ledger own the
// data). Money is USD cents end to end; latency is milliseconds; time bounds are
// POSITIONAL parameters (never interpolated), and the bucket interval is a
// server-side constant — injection-safe.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
)

// Fully-qualified datastore tables. admin only READS these — the ZAP receivers
// (event.span/event.log, planesink.go), the ai ledger (hanzo.cloud_usage), and the
// event plane (apps/analytics) own their writes.
//
// The AI lens reads the PLANE's span table: a gen_ai span IS the observation of
// record (HIP-0132; llmobs in hanzoai/o11y projects the very same attributes).
// This const has hopped twice, each hop toward the rows that actually exist:
// `o11y_ai.observations` (a database that never existed — the panel read zero),
// then `console.observations` (real rows, but a SURFACE name on a store that had
// already been folded into the plane). event.span holds those same rows as
// kind='client' gen_ai spans — identical count (8,867) and identical summed cost
// verified against console on 2026-07-31 — so with this hop nothing reads
// `console` and the database is droppable. Model, cost and latency are span
// ATTRIBUTES (gen_ai.* / _o11y.*), not columns, hence the projection consts next
// to the table name; attribute values are Map strings, so numeric ones read
// through toFloat64OrZero.
const (
	o11yTraceTable = "event.span"
	o11yLogTable   = "event.log"

	// The fleet AI observation source: gen_ai spans on the event plane. These
	// consts are ONE projection stated ONCE — aimetrics.go reads them too. (Its
	// former twin const drifted into pointing at nothing precisely because the
	// same fact was stated twice.) kind='client' because the observation is the
	// LLM CALL span — OTel gen_ai spans are client spans, and that is the kind
	// carrying gen_ai.operation.name/model/cost; the old trace roots live beside
	// them as kind='server' gen_ai spans and are NOT observations.
	o11yAIObs      = "event.span"
	o11yGenAISpan  = "mapContains(attributes, 'gen_ai.system') AND kind = 'client'"
	o11yGenAICost  = "toFloat64OrZero(attributes['_o11y.gen_ai.total_cost'])"
	o11yGenAIModel = "if(attributes['gen_ai.response.model'] != '', " +
		"attributes['gen_ai.response.model'], attributes['gen_ai.request.model'])"

	o11yTopN         = 10
	o11yServiceLimit = 12

	// o11yServiceCol is event.span's native service column, o11yDurationCol its span
	// duration (UInt64 nanoseconds). The plane spells them plainly — `service` and
	// `duration` — NOT the o11y v3 index's `resource_string_service$$name` /
	// `duration_nano`, nor the v2 index's `serviceName` / `durationNano`. Naming a
	// column that does not exist does not error loudly here: the query fails, the
	// caller's `if err == nil` swallows it, and the whole trace half of the board
	// renders honest-looking zeros forever. Pinned as constants so the two queries
	// below and the per-subsystem board all spell them once.
	o11yServiceCol  = "service"
	o11yDurationCol = "duration"
)

// o11yGlobal is the whole fleet o11y board payload.
type o11yGlobal struct {
	// Range is the window every figure below covers: 24h, 7d or 30d. It is the
	// NORMALIZED value — an unrecognized ?range reads as 30d and says so here.
	Range string `json:"range"`
	// Start is the window's lower bound, RFC 3339 UTC, derived from Range.
	Start string `json:"start"`
	// End is the moment of this read, RFC 3339 UTC.
	End string `json:"end"`
	// Totals is the KPI band: the usage half, the trace half and log volume.
	Totals o11yTotals `json:"totals"`
	// Series is usage bucketed over the window, oldest first. A bucket with no
	// traffic has no point — the series is not padded to a fixed length.
	Series []o11ySeries `json:"series"`
	// LogSeries is log volume over the SAME buckets as Series, so the two line up.
	LogSeries []o11yLogPoint `json:"logSeries"`
	// TopOrgs is the ten busiest tenants by request count.
	TopOrgs []o11yOrgStat `json:"topOrgs"`
	// TopModels is the ten busiest models by request count.
	TopModels []o11yModelStat `json:"topModels"`
	// TopServices is the twelve busiest services by trace count.
	TopServices []o11ySvcStat `json:"topServices"`
	// LLM is the generation rollup over gen_ai spans. Its money is US DOLLARS,
	// unlike Totals above — the two come from different planes and each keeps its
	// plane's unit.
	LLM o11yLLM `json:"llm"`
}

// o11yTotals is the fleet KPI band. LLM half from cloud_usage; RED half from traces;
// volume from logs. Every field is a real aggregate or an honest zero.
type o11yTotals struct {
	// Requests is how many LLM calls the fleet served in the window, across every
	// tenant, errored ones included. From the billing ledger.
	Requests int64 `json:"requests"`
	// Tokens is prompt plus completion tokens across those calls.
	Tokens int64 `json:"tokens"`
	// PromptTokens is the input half of Tokens.
	PromptTokens int64 `json:"promptTokens"`
	// CompletionTokens is the generated half of Tokens.
	CompletionTokens int64 `json:"completionTokens"`
	// CostCents is what those calls cost, in US CENTS. The trace and gen_ai
	// figures elsewhere on this board are not in cents; this one is.
	CostCents int64 `json:"costCents"`
	// Errors is how many of Requests the ledger marked failed.
	Errors int64 `json:"errors"`
	// Orgs is how many DISTINCT tenants appear in the window — the fleet's active
	// tenant count, not a count of calls.
	Orgs int64 `json:"orgs"`
	// Models is how many distinct models were called.
	Models int64 `json:"models"`
	// TraceCount is how many spans the fleet emitted in the window, across every
	// service. It counts TRACED REQUESTS, so it is a much larger number than
	// Requests above and is not a superset of it: one is the trace plane, the other
	// the billing ledger.
	TraceCount int64 `json:"traceCount"`
	// LatencyP50Ms is the median request duration in milliseconds, over spans
	// rather than over ledger rows — so it covers every traced request, not only
	// the LLM ones counted above.
	LatencyP50Ms float64 `json:"latencyP50Ms"`
	// LatencyP95Ms is the 95th percentile of the same durations.
	LatencyP95Ms float64 `json:"latencyP95Ms"`
	// LatencyP99Ms is the 99th percentile — the tail worth paging on.
	LatencyP99Ms   float64 `json:"latencyP99Ms"`
	TraceErrorRate float64 `json:"traceErrorRate"` // percent (0..100)
	// Services is how many distinct services emitted a span in the window.
	Services int64 `json:"services"`
	// LogVolume is how many log lines the fleet emitted in the window. Volume only
	// — nothing here says how many of them were errors.
	LogVolume int64 `json:"logVolume"`
}

// o11ySeries is one usage time bucket (fleet-wide).
type o11ySeries struct {
	// Ts is the bucket's start, RFC 3339 UTC. The width follows the range — an
	// hour at 24h, six hours at 7d, a day at 30d — so it is not a fixed unit.
	Ts string `json:"ts"`
	// Requests is the calls served in that bucket.
	Requests int64 `json:"requests"`
	// Tokens is the tokens they consumed.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost, in US cents.
	CostCents int64 `json:"costCents"`
	// Errors is how many of them failed.
	Errors int64 `json:"errors"`
}

// o11yLogPoint is one log-volume time bucket (fleet-wide).
type o11yLogPoint struct {
	// Ts is the bucket's start, RFC 3339 UTC, on the same buckets as the usage
	// series so the two can be read together.
	Ts string `json:"ts"`
	// Count is the log lines emitted in that bucket, fleet-wide.
	Count int64 `json:"count"`
}

// o11yOrgStat is one row of the top-orgs-by-usage leaderboard.
type o11yOrgStat struct {
	// Org is the tenant slug.
	Org string `json:"org"`
	// Requests is how many calls that tenant made. The board ranks on this, so the
	// top rows are the busiest tenants and not necessarily the costliest.
	Requests int64 `json:"requests"`
	// Tokens is what those calls consumed.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost, in US cents.
	CostCents int64 `json:"costCents"`
}

// o11yModelStat is one row of the top-models leaderboard.
type o11yModelStat struct {
	// Model is the model id the ledger recorded. Unattributed rows carry no model
	// and are left out rather than ranked as a nameless one.
	Model string `json:"model"`
	// Requests is how many calls went to it. The board ranks on this.
	Requests int64 `json:"requests"`
	// Tokens is what those calls consumed.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost, in US cents.
	CostCents int64 `json:"costCents"`
}

// o11ySvcStat is one row of the top-services (by trace volume) leaderboard.
type o11ySvcStat struct {
	// Service is the emitting service's name, from the span's own service column.
	// The whole cloud binary reports under ONE such name however many subsystems
	// it mounts, which is what the per-subsystem board next door exists to split
	// apart.
	Service string `json:"service"`
	// Requests is how many spans it emitted in the window. The board ranks on
	// this.
	Requests  int64   `json:"requests"`
	ErrorRate float64 `json:"errorRate"` // percent (0..100)
	// LatencyP95Ms is that service's 95th-percentile span duration in
	// milliseconds.
	LatencyP95Ms float64 `json:"latencyP95Ms"`
}

// o11yLLM is the fleet-wide LLM generation rollup over gen_ai spans.
type o11yLLM struct {
	// Generations is how many LLM calls the fleet made in the window — one per
	// gen_ai span. It counts the same activity the usage totals do, from the trace
	// plane instead of the billing ledger, so the two need not match exactly.
	Generations int64 `json:"generations"`
	// CostUsd is their summed cost in US DOLLARS, not cents: the span attribute's
	// native unit, carried through unconverted.
	CostUsd float64 `json:"costUsd"`
}

// o11y is the fleet-wide observability board: LLM usage (requests, tokens, cost,
// errors, top orgs, top models), trace RED metrics (count, p50/p95/p99 latency in ms,
// error rate, top services), fleet log volume, and the O11yAI generation rollup — all
// aggregated across EVERY tenant, with no org filter applied.
//
// Every signal degrades INDEPENDENTLY. A table that is absent or errors contributes its
// zero value and the read still succeeds, so the board renders exactly what the
// warehouse holds rather than failing whole because one of four sources is missing.
// Same when the warehouse is not connected at all: the zero board, never a fabricated
// fleet.
//
// Example: {"range":"7d"}
// Response: {"status":"ok","msg":"","data":{"range":"7d","start":"2026-07-20T00:00:00Z",
// "end":"2026-07-27T00:00:00Z","totals":{"requests":10420,"tokens":8100000,"costCents":41200,
// "errors":37},"series":[],"logSeries":[],"topOrgs":[],"topModels":[],"topServices":[],
// "llm":{"generations":0,"costUsd":0}}}
func o11y(ctx context.Context, in *rangeIn) (*o11yOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	rangeLabel := o11yRange(in.Range)
	since := computeSince(rangeLabel)
	payload := o11yGlobal{
		Range:       rangeLabel,
		Start:       since.Format(time.RFC3339),
		End:         time.Now().UTC().Format(time.RFC3339),
		Series:      []o11ySeries{},
		LogSeries:   []o11yLogPoint{},
		TopOrgs:     []o11yOrgStat{},
		TopModels:   []o11yModelStat{},
		TopServices: []o11ySvcStat{},
	}

	// Honest-empty when the warehouse is not connected: the board renders its zero
	// state, never a fabricated fleet.
	if !datastore.Ready() {
		return &o11yOut{Status: core.OK, Data: &payload}, nil
	}

	// ONE DateTime bound for every source: hanzo.cloud_usage keys on `timestamp`,
	// and the plane's event.span / event.log both key on a `time` DateTime64(9)
	// column — a single DateTime literal binds against all three.
	sinceTS := chTS(since)
	interval := o11yBucket(rangeLabel)
	// The ledger reads share ONE scope value (ledger.go) — this board is fleet-wide,
	// so it names no tenant.
	fleet := ledgerScope{Since: since}

	// LLM usage totals (all orgs) — the shared ledger reads live in ledger.go.
	fillUsageTotals(&payload.Totals, firstRowOr(ledgerRows(ctx, ledgerTotals(fleet))))
	// Trace RED metrics (all services).
	if rows, err := datastore.Query(ctx, o11yTraceTotalsSQL(), sinceTS); err == nil {
		fillTraceTotals(&payload.Totals, firstRowOr(rows))
	}
	// Fleet log volume.
	if rows, err := datastore.Query(ctx, o11yLogVolumeSQL(), sinceTS); err == nil {
		payload.Totals.LogVolume = chInt64(firstRowOr(rows)["c"])
	}
	// Usage time-series (fleet).
	payload.Series = usageSeriesFromRows(ledgerRows(ctx, ledgerSeries(fleet, interval)))
	// Log-volume time-series (fleet).
	if rows, err := datastore.Query(ctx, o11yLogSeriesSQL(interval), sinceTS); err == nil {
		payload.LogSeries = logSeriesFromRows(rows)
	}
	// Top orgs by usage.
	payload.TopOrgs = topOrgsFromRows(ledgerRows(ctx, ledgerByOrg(fleet, o11yTopN)))
	// Top models by usage.
	payload.TopModels = topModelsFromRows(ledgerRows(ctx, ledgerByModel(fleet, o11yTopN)))
	// Top services by trace volume.
	if rows, err := datastore.Query(ctx, o11yTopServicesSQL(), sinceTS); err == nil {
		payload.TopServices = topServicesFromRows(rows)
	}
	// Fleet LLM generations — gen_ai spans on the plane; best-effort.
	if rows, err := datastore.Query(ctx, o11yLLMSQL(), sinceTS); err == nil {
		r := firstRowOr(rows)
		payload.LLM = o11yLLM{Generations: chInt64(r["gens"]), CostUsd: chFloat64(r["cost"])}
	}

	return &o11yOut{Status: core.OK, Data: &payload}, nil
}

// o11yOut is the GET /v1/admin/o11y envelope.
type o11yOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the board. A warehouse that is not connected, or a table that is
	// missing, yields the ZERO board rather than a failure — so zeros here mean
	// either a quiet fleet or a blind one, and this envelope cannot tell you
	// which.
	Data *o11yGlobal `json:"data"`
}

// ── pure SQL builders (static SQL + one positional time bound; unit-tested) ──

func o11yTraceTotalsSQL() string {
	return "SELECT count() AS traces, " +
		"round(quantile(0.5)(" + o11yDurationCol + ") / 1e6, 2) AS p50, " +
		"round(quantile(0.95)(" + o11yDurationCol + ") / 1e6, 2) AS p95, " +
		"round(quantile(0.99)(" + o11yDurationCol + ") / 1e6, 2) AS p99, " +
		"round(100 * countIf(status = 'error') / greatest(count(), 1), 3) AS err_rate, " +
		"uniqExact(" + o11yServiceCol + ") AS services " +
		"FROM " + o11yTraceTable + " WHERE time >= ?"
}

func o11yLogVolumeSQL() string {
	return "SELECT count() AS c FROM " + o11yLogTable + " WHERE time >= ?"
}

func o11yLogSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(time, INTERVAL " + interval + ") AS ts, " +
		"count() AS c FROM " + o11yLogTable + " WHERE time >= ? GROUP BY ts ORDER BY ts"
}

func o11yTopServicesSQL() string {
	return "SELECT " + o11yServiceCol + " AS service, count() AS requests, " +
		"round(100 * countIf(status = 'error') / greatest(count(), 1), 3) AS error_rate, " +
		"round(quantile(0.95)(" + o11yDurationCol + ") / 1e6, 2) AS p95 " +
		"FROM " + o11yTraceTable + " WHERE time >= ? AND " + o11yServiceCol + " != '' " +
		"GROUP BY service ORDER BY requests DESC LIMIT " + strconv.Itoa(o11yServiceLimit)
}

// o11yLLMSQL is the fleet generation rollup over gen_ai spans — ONE builder,
// read by this board and by aimetrics (same package, same query, stated once).
func o11yLLMSQL() string {
	return "SELECT count() AS gens, sum(" + o11yGenAICost + ") AS cost FROM " + o11yAIObs +
		" WHERE " + o11yGenAISpan + " AND time >= ?"
}

// ── pure row parsers (unit-tested) ──

func fillUsageTotals(t *o11yTotals, r map[string]any) {
	t.Requests = chInt64(r["requests"])
	t.Tokens = chInt64(r["tokens"])
	t.PromptTokens = chInt64(r["prompt_tokens"])
	t.CompletionTokens = chInt64(r["completion_tokens"])
	t.CostCents = chInt64(r["cost_cents"])
	t.Errors = chInt64(r["errors"])
	t.Orgs = chInt64(r["orgs"])
	t.Models = chInt64(r["models"])
}

func fillTraceTotals(t *o11yTotals, r map[string]any) {
	t.TraceCount = chInt64(r["traces"])
	t.LatencyP50Ms = chFloat64(r["p50"])
	t.LatencyP95Ms = chFloat64(r["p95"])
	t.LatencyP99Ms = chFloat64(r["p99"])
	t.TraceErrorRate = chFloat64(r["err_rate"])
	t.Services = chInt64(r["services"])
}

func usageSeriesFromRows(rows []map[string]any) []o11ySeries {
	out := make([]o11ySeries, 0, len(rows))
	for _, r := range rows {
		out = append(out, o11ySeries{
			Ts:        chTime(r["ts"]),
			Requests:  chInt64(r["requests"]),
			Tokens:    chInt64(r["tokens"]),
			CostCents: chInt64(r["cost_cents"]),
			Errors:    chInt64(r["errors"]),
		})
	}
	return out
}

func logSeriesFromRows(rows []map[string]any) []o11yLogPoint {
	out := make([]o11yLogPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, o11yLogPoint{Ts: chTime(r["ts"]), Count: chInt64(r["c"])})
	}
	return out
}

func topOrgsFromRows(rows []map[string]any) []o11yOrgStat {
	out := make([]o11yOrgStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, o11yOrgStat{
			Org:       chStr(r["org"]),
			Requests:  chInt64(r["requests"]),
			Tokens:    chInt64(r["tokens"]),
			CostCents: chInt64(r["cost_cents"]),
		})
	}
	return out
}

func topModelsFromRows(rows []map[string]any) []o11yModelStat {
	out := make([]o11yModelStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, o11yModelStat{
			Model:     chStr(r["model"]),
			Requests:  chInt64(r["requests"]),
			Tokens:    chInt64(r["tokens"]),
			CostCents: chInt64(r["cost_cents"]),
		})
	}
	return out
}

func topServicesFromRows(rows []map[string]any) []o11ySvcStat {
	out := make([]o11ySvcStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, o11ySvcStat{
			Service:      chStr(r["service"]),
			Requests:     chInt64(r["requests"]),
			ErrorRate:    chFloat64(r["error_rate"]),
			LatencyP95Ms: chFloat64(r["p95"]),
		})
	}
	return out
}

// ── small pure helpers ──

// o11yRange normalizes the ?range enum (default 30d).
func o11yRange(v string) string {
	switch strings.TrimSpace(v) {
	case "24h":
		return "24h"
	case "7d":
		return "7d"
	default:
		return "30d"
	}
}

// o11yBucket maps the range to a fixed datastore interval clause (a server-side
// CONSTANT — never user input — so it is safe to render into the SQL). ~24-30
// buckets across the window keeps the charts legible.
func o11yBucket(rangeLabel string) string {
	switch rangeLabel {
	case "24h":
		return "1 HOUR"
	case "7d":
		return "6 HOUR"
	default:
		return "1 DAY"
	}
}

// firstRowOr returns the first row or an empty map (never nil), so a parser reads
// honest zeros from an empty result instead of panicking.
func firstRowOr(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return map[string]any{}
	}
	return rows[0]
}

// chFloat64 coerces a datastore numeric cell to float64 (the round()/quantile()
// columns land as float64; a Decimal serialized to string is parsed). The twin of
// chInt64 for the latency/error-rate/cost fields. Non-numeric → 0 (honest zero).
func chFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}
