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
// Signals, each from its canonical table in the one datastore:
//   - LLM usage  → hanzo.cloud_usage : requests, tokens, cost, errors, top orgs, top models
//   - Spans      → event.span        : request count, latency p50/p95/p99, error rate,
//                                      top services
//   - Logs       → event.log         : fleet log volume + volume-over-time
//   - LLM gens   → event.span        : the gen_ai spans ai/object emits — generations and
//                                      provider cost, fleet-wide
//
// The gen_ai rollup reads the SPAN plane, not a table of its own. A generation IS
// a span the ai module already emits (gen_ai.system, gen_ai.request.model, the
// _o11y.gen_ai.* cost attributes), so a second table for it would be a second
// ingestion contract for one fact — and the one that used to be named here,
// o11y_ai.observations, has never existed on this warehouse, which is why this
// half of the board read a permanent honest zero.
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

// admin only READS these — the ai ledger (hanzo.cloud_usage) and the event-plane
// writers own their writes. The event tables are named once, in apps/datastore,
// beside the connection every one of these queries runs on.
const (
	o11yUsageTable   = "hanzo.cloud_usage"
	o11yTopN         = 10
	o11yServiceLimit = 12

	// service and duration are ENVELOPE/signal columns on event.span, so they are
	// spelled as themselves. They used to be `resource_string_service$$name` and
	// `duration_nano` — a materialized resource attribute and a unit-suffixed name —
	// and getting either wrong did not error loudly: the query failed, the caller's
	// `if err == nil` swallowed it, and the whole span half of the board rendered
	// honest-looking zeros forever. That failure mode is why they were pinned as
	// constants; the names are now plain enough that the constants are gone.
	//
	// genAISystem is the attribute every gen_ai span carries (ai/object stamps
	// gen_ai.system), and genAICost the provider cost it stamps in dollars. They
	// are the ONE way a generation is distinguished from any other span.
	genAISystem = "gen_ai.system"
	genAICost   = "_o11y.gen_ai.total_cost"
)

// o11yGlobal is the whole fleet o11y board payload.
type o11yGlobal struct {
	Range       string          `json:"range"`
	Start       string          `json:"start"`
	End         string          `json:"end"`
	Totals      o11yTotals      `json:"totals"`
	Series      []o11ySeries    `json:"series"`
	LogSeries   []o11yLogPoint  `json:"logSeries"`
	TopOrgs     []o11yOrgStat   `json:"topOrgs"`
	TopModels   []o11yModelStat `json:"topModels"`
	TopServices []o11ySvcStat   `json:"topServices"`
	LLM         o11yLLM         `json:"llm"`
}

// o11yTotals is the fleet KPI band. LLM half from cloud_usage; RED half from traces;
// volume from logs. Every field is a real aggregate or an honest zero.
type o11yTotals struct {
	// LLM usage (hanzo.cloud_usage), all orgs.
	Requests         int64 `json:"requests"`
	Tokens           int64 `json:"tokens"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	CostCents        int64 `json:"costCents"`
	Errors           int64 `json:"errors"`
	Orgs             int64 `json:"orgs"`
	Models           int64 `json:"models"`
	// Spans (event.span), all services.
	TraceCount     int64   `json:"traceCount"`
	LatencyP50Ms   float64 `json:"latencyP50Ms"`
	LatencyP95Ms   float64 `json:"latencyP95Ms"`
	LatencyP99Ms   float64 `json:"latencyP99Ms"`
	TraceErrorRate float64 `json:"traceErrorRate"` // percent (0..100)
	Services       int64   `json:"services"`
	// Logs (event.log), fleet volume over the window.
	LogVolume int64 `json:"logVolume"`
}

// o11ySeries is one usage time bucket (fleet-wide).
type o11ySeries struct {
	Ts        string `json:"ts"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
	Errors    int64  `json:"errors"`
}

// o11yLogPoint is one log-volume time bucket (fleet-wide).
type o11yLogPoint struct {
	Ts    string `json:"ts"`
	Count int64  `json:"count"`
}

// o11yOrgStat is one row of the top-orgs-by-usage leaderboard.
type o11yOrgStat struct {
	Org       string `json:"org"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

// o11yModelStat is one row of the top-models leaderboard.
type o11yModelStat struct {
	Model     string `json:"model"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

// o11ySvcStat is one row of the top-services (by trace volume) leaderboard.
type o11ySvcStat struct {
	Service      string  `json:"service"`
	Requests     int64   `json:"requests"`
	ErrorRate    float64 `json:"errorRate"` // percent (0..100)
	LatencyP95Ms float64 `json:"latencyP95Ms"`
}

// o11yLLM is the fleet-wide gen_ai generation rollup, read off the span plane.
type o11yLLM struct {
	Generations int64   `json:"generations"`
	CostUsd     float64 `json:"costUsd"`
}

// o11y is the fleet-wide observability board. It carries LLM usage (requests, tokens,
// cost, errors, top orgs, top models), trace RED metrics (count, p50/p95/p99 latency in
// ms, error rate, top services), fleet log volume, and the gen_ai generation rollup — all
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

	// ONE time bound for the whole board. Every event-plane table spells its
	// instant `time` as a DateTime64, so the same literal binds against spans and
	// logs alike — the log half used to need a separate UInt64-nanosecond bound
	// because its old table stored the instant as a raw integer.
	sinceTS := chTS(since)
	interval := o11yBucket(rangeLabel)

	// LLM usage totals (all orgs).
	if rows, err := datastore.Query(ctx, o11yUsageTotalsSQL(), sinceTS); err == nil {
		fillUsageTotals(&payload.Totals, firstRowOr(rows))
	}
	// Span RED metrics (all services).
	if rows, err := datastore.Query(ctx, o11yTraceTotalsSQL(), sinceTS); err == nil {
		fillTraceTotals(&payload.Totals, firstRowOr(rows))
	}
	// Fleet log volume.
	if rows, err := datastore.Query(ctx, o11yLogVolumeSQL(), sinceTS); err == nil {
		payload.Totals.LogVolume = chInt64(firstRowOr(rows)["c"])
	}
	// Usage time-series (fleet).
	if rows, err := datastore.Query(ctx, o11yUsageSeriesSQL(interval), sinceTS); err == nil {
		payload.Series = usageSeriesFromRows(rows)
	}
	// Log-volume time-series (fleet).
	if rows, err := datastore.Query(ctx, o11yLogSeriesSQL(interval), sinceTS); err == nil {
		payload.LogSeries = logSeriesFromRows(rows)
	}
	// Top orgs by usage.
	if rows, err := datastore.Query(ctx, o11yTopOrgsSQL(), sinceTS); err == nil {
		payload.TopOrgs = topOrgsFromRows(rows)
	}
	// Top models by usage.
	if rows, err := datastore.Query(ctx, o11yTopModelsSQL(), sinceTS); err == nil {
		payload.TopModels = topModelsFromRows(rows)
	}
	// Top services by trace volume.
	if rows, err := datastore.Query(ctx, o11yTopServicesSQL(), sinceTS); err == nil {
		payload.TopServices = topServicesFromRows(rows)
	}
	// Fleet gen_ai generations — best-effort, off the span plane.
	if rows, err := datastore.Query(ctx, o11yLLMSQL(), sinceTS); err == nil {
		r := firstRowOr(rows)
		payload.LLM = o11yLLM{Generations: chInt64(r["gens"]), CostUsd: chFloat64(r["cost"])}
	}

	return &o11yOut{Status: core.OK, Data: &payload}, nil
}

// o11yOut is the GET /v1/admin/o11y envelope.
type o11yOut struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   *o11yGlobal `json:"data"`
}

// ── pure SQL builders (static SQL + one positional time bound; unit-tested) ──

func o11yUsageTotalsSQL() string {
	return "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, countIf(status = 'error') AS errors, " +
		"uniqExact(organization) AS orgs, uniqExact(model) AS models " +
		"FROM " + o11yUsageTable + " WHERE timestamp >= ?"
}

func o11yTraceTotalsSQL() string {
	return "SELECT count() AS traces, " +
		"round(quantile(0.5)(duration) / 1e6, 2) AS p50, " +
		"round(quantile(0.95)(duration) / 1e6, 2) AS p95, " +
		"round(quantile(0.99)(duration) / 1e6, 2) AS p99, " +
		"round(100 * countIf(status = '" + datastore.SpanError + "') / greatest(count(), 1), 3) AS err_rate, " +
		"uniqExact(service) AS services " +
		"FROM " + datastore.Span + " WHERE time >= ?"
}

func o11yLogVolumeSQL() string {
	return "SELECT count() AS c FROM " + datastore.Log + " WHERE time >= ?"
}

func o11yUsageSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(timestamp, INTERVAL " + interval + ") AS ts, " +
		"count() AS requests, sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents, " +
		"countIf(status = 'error') AS errors " +
		"FROM " + o11yUsageTable + " WHERE timestamp >= ? GROUP BY ts ORDER BY ts"
}

func o11yLogSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(time, INTERVAL " + interval + ") AS ts, " +
		"count() AS c FROM " + datastore.Log + " WHERE time >= ? GROUP BY ts ORDER BY ts"
}

func o11yTopOrgsSQL() string {
	return "SELECT organization AS org, count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + o11yUsageTable +
		" WHERE timestamp >= ? GROUP BY org ORDER BY requests DESC LIMIT " + strconv.Itoa(o11yTopN)
}

func o11yTopModelsSQL() string {
	return "SELECT model, count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + o11yUsageTable +
		" WHERE timestamp >= ? AND model != '' GROUP BY model ORDER BY requests DESC LIMIT " + strconv.Itoa(o11yTopN)
}

func o11yTopServicesSQL() string {
	return "SELECT service, count() AS requests, " +
		"round(100 * countIf(status = '" + datastore.SpanError + "') / greatest(count(), 1), 3) AS error_rate, " +
		"round(quantile(0.95)(duration) / 1e6, 2) AS p95 " +
		"FROM " + datastore.Span + " WHERE time >= ? AND service != '' " +
		"GROUP BY service ORDER BY requests DESC LIMIT " + strconv.Itoa(o11yServiceLimit)
}

// o11yLLMSQL rolls up the fleet's generations from the gen_ai spans in the span
// plane. A generation is a span, so this is the same table every other signal on
// this board reads — one plane, one time column, one predicate shape. The cost
// attribute is in dollars.
func o11yLLMSQL() string {
	return "SELECT count() AS gens, sum(toFloat64OrZero(attributes['" + genAICost + "'])) AS cost " +
		"FROM " + datastore.Span + " WHERE time >= ? AND attributes['" + genAISystem + "'] != ''"
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
