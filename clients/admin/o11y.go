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
// ONE hanzoai/datastore (ClickHouse) — the same warehouse + shared client
// (aiobject.DatastoreQuery) the analytics/compute lenses already use, no second
// connection.
//
// Signals, each from its canonical table in the one datastore:
//   - LLM usage  → hanzo.cloud_usage         : requests, tokens, cost, errors, top orgs, top models
//   - Traces     → o11y_traces.distributed_o11y_index_v3 : request count, latency p50/p95/p99,
//                                                              error rate, top services
//   - Logs       → o11y_logs.distributed_logs_v2 : fleet log volume + volume-over-time
//   - LLM gens   → langfuse.observations      : generations + cost (fleet-wide; honest-empty today)
//
// GLOBAL-ADMIN ONLY (the s.guard wrap in admin.go): the gateway strips a client
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
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Fully-qualified datastore tables. admin only READS these — the ZAP collector
// (o11y_*), the ai ledger (hanzo.cloud_usage), and Langfuse own their writes.
const (
	o11yUsageTable   = "hanzo.cloud_usage"
	o11yTraceTable   = "o11y_traces.distributed_o11y_index_v3"
	o11yLogTable     = "o11y_logs.distributed_logs_v2"
	o11yLangfuseObs  = "langfuse.observations"
	o11yTopN         = 10
	o11yServiceLimit = 12
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
	// Traces (o11y_index_v3), all services.
	TraceCount     int64   `json:"traceCount"`
	LatencyP50Ms   float64 `json:"latencyP50Ms"`
	LatencyP95Ms   float64 `json:"latencyP95Ms"`
	LatencyP99Ms   float64 `json:"latencyP99Ms"`
	TraceErrorRate float64 `json:"traceErrorRate"` // percent (0..100)
	Services       int64   `json:"services"`
	// Logs (distributed_logs_v2), fleet volume over the window.
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

// o11yLLM is the fleet-wide Langfuse generation rollup (near-empty today → honest).
type o11yLLM struct {
	Generations int64   `json:"generations"`
	CostUsd     float64 `json:"costUsd"`
}

// o11y answers GET /v1/admin/o11y. ?range=24h|7d|30d bounds the window (default 30d).
// GLOBAL-ADMIN ONLY (s.guard). Every signal degrades independently: a table that is
// absent or errors contributes its zero-value, never a failure — the fleet board
// always renders what the datastore actually holds.
func o11y(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	rangeLabel := o11yRange(c.Query("range"))
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
	if !aiobject.DatastoreEnabled() {
		return ok(c, payload)
	}

	sinceTS := chTS(since)         // DateTime literal — cloud_usage.timestamp, traces.timestamp
	sinceNanos := since.UnixNano() // UInt64 nanos — logs.timestamp
	interval := o11yBucket(rangeLabel)

	// LLM usage totals (all orgs).
	if rows, err := aiobject.DatastoreQuery(ctx, o11yUsageTotalsSQL(), sinceTS); err == nil {
		fillUsageTotals(&payload.Totals, firstRowOr(rows))
	}
	// Trace RED metrics (all services).
	if rows, err := aiobject.DatastoreQuery(ctx, o11yTraceTotalsSQL(), sinceTS); err == nil {
		fillTraceTotals(&payload.Totals, firstRowOr(rows))
	}
	// Fleet log volume.
	if rows, err := aiobject.DatastoreQuery(ctx, o11yLogVolumeSQL(), sinceNanos); err == nil {
		payload.Totals.LogVolume = chInt64(firstRowOr(rows)["c"])
	}
	// Usage time-series (fleet).
	if rows, err := aiobject.DatastoreQuery(ctx, o11yUsageSeriesSQL(interval), sinceTS); err == nil {
		payload.Series = usageSeriesFromRows(rows)
	}
	// Log-volume time-series (fleet).
	if rows, err := aiobject.DatastoreQuery(ctx, o11yLogSeriesSQL(interval), sinceNanos); err == nil {
		payload.LogSeries = logSeriesFromRows(rows)
	}
	// Top orgs by usage.
	if rows, err := aiobject.DatastoreQuery(ctx, o11yTopOrgsSQL(), sinceTS); err == nil {
		payload.TopOrgs = topOrgsFromRows(rows)
	}
	// Top models by usage.
	if rows, err := aiobject.DatastoreQuery(ctx, o11yTopModelsSQL(), sinceTS); err == nil {
		payload.TopModels = topModelsFromRows(rows)
	}
	// Top services by trace volume.
	if rows, err := aiobject.DatastoreQuery(ctx, o11yTopServicesSQL(), sinceTS); err == nil {
		payload.TopServices = topServicesFromRows(rows)
	}
	// Fleet LLM generations (Langfuse) — best-effort; near-empty today.
	if rows, err := aiobject.DatastoreQuery(ctx, o11yLLMSQL(), sinceTS); err == nil {
		r := firstRowOr(rows)
		payload.LLM = o11yLLM{Generations: chInt64(r["gens"]), CostUsd: chFloat64(r["cost"])}
	}

	return ok(c, payload)
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
		"round(quantile(0.5)(durationNano) / 1e6, 2) AS p50, " +
		"round(quantile(0.95)(durationNano) / 1e6, 2) AS p95, " +
		"round(quantile(0.99)(durationNano) / 1e6, 2) AS p99, " +
		"round(100 * countIf(has_error) / greatest(count(), 1), 3) AS err_rate, " +
		"uniqExact(serviceName) AS services " +
		"FROM " + o11yTraceTable + " WHERE timestamp >= ?"
}

func o11yLogVolumeSQL() string {
	return "SELECT count() AS c FROM " + o11yLogTable + " WHERE timestamp >= ?"
}

func o11yUsageSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(timestamp, INTERVAL " + interval + ") AS ts, " +
		"count() AS requests, sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents, " +
		"countIf(status = 'error') AS errors " +
		"FROM " + o11yUsageTable + " WHERE timestamp >= ? GROUP BY ts ORDER BY ts"
}

func o11yLogSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(toDateTime(timestamp / 1000000000), INTERVAL " + interval + ") AS ts, " +
		"count() AS c FROM " + o11yLogTable + " WHERE timestamp >= ? GROUP BY ts ORDER BY ts"
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
	return "SELECT serviceName AS service, count() AS requests, " +
		"round(100 * countIf(has_error) / greatest(count(), 1), 3) AS error_rate, " +
		"round(quantile(0.95)(durationNano) / 1e6, 2) AS p95 " +
		"FROM " + o11yTraceTable + " WHERE timestamp >= ? AND serviceName != '' " +
		"GROUP BY service ORDER BY requests DESC LIMIT " + strconv.Itoa(o11yServiceLimit)
}

func o11yLLMSQL() string {
	return "SELECT count() AS gens, toFloat64(sum(total_cost)) AS cost FROM " + o11yLangfuseObs +
		" WHERE type = 'GENERATION' AND start_time >= ?"
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

// o11yBucket maps the range to a fixed ClickHouse interval clause (a server-side
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

// chFloat64 coerces a ClickHouse numeric cell to float64 (the round()/quantile()
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
