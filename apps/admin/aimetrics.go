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

// aimetrics — GET /v1/admin/aimetrics, the GLOBAL fleet-wide AI / training / eval
// read that powers the operator's AI-metrics board on admin.hanzo.ai. It is the
// AI-and-eval-focused companion to o11y (o11y.go): where o11y answers "how is the
// FLEET behaving" (RED metrics, logs, usage), this answers "how are the MODELS and
// EVALS doing" — LLM generations, per-model spend, and eval-run quality/progress —
// over the SAME ONE datastore (Datastore), the SAME shared client
// (datastore.Query), no second connection.
//
// Signals, each from its canonical table in the one datastore:
//   - LLM generations → event.span            : gen_ai spans — generations, cost
//                                                (USD) and latency projected from
//                                                span attributes; a gen_ai span IS
//                                                the observation (see o11y.go)
//   - Per-model usage → hanzo.cloud_usage      : requests, tokens, cost per model
//                                                (the live usage ledger the ai gateway
//                                                writes — populated today)
//   - Eval runs       → hanzo.eval_traces      : traces, runs, datasets, models under
//                                                test, per-trace latency
//   - Eval progress   → hanzo.eval_scores      : score count, avg score, per-score-name
//                                                distribution, recent-run averages, and
//                                                the avg-score-over-time TREND — the
//                                                training/eval progress signal
//
// The eval_traces / eval_scores tables are OWNED and written by the eval telemetry
// store (apps/eval/telemetry.go) — the SAME warehouse, same db ("hanzo"), same
// shared aiobject client. admin only READS them here. There is deliberately no
// "training_progress" table: the router's per-request training events live in the ai
// OLTP Postgres (object.RoutingEvent), NOT the OLAP warehouse, so the honest
// warehouse-side progress signal is the eval-score trend, not a routing table.
//
// SUPERADMIN ONLY (core.Admit, the op's first line), all-orgs, no org filter — the
// one place a fleet operator crosses tenants for AI/eval metrics; a non-admin bearer
// is refused 403 before a single row is read. Fail-closed.
//
// Honest by construction, exactly like o11y/compute: no datastore connected → the
// real empty aggregate, never a fabricated fleet; and every signal degrades
// INDEPENDENTLY — a table that is absent or a column that differs contributes its
// zero-value (the enclosing `if err == nil`), never a failure, so the board always
// renders what the datastore actually holds. admin READS only; it owns and creates
// NO table. Money from cloud_usage is USD cents, from gen_ai spans is USD; latency
// is milliseconds; time bounds are POSITIONAL parameters (never interpolated), and
// the bucket interval is a server-side constant — injection-safe.

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
)

// Fully-qualified datastore tables. admin only READS these — the ai gateway owns
// hanzo.cloud_usage, the event plane (apps/analytics) owns event.span, and the
// eval telemetry store (clients/eval) owns hanzo.eval_traces / hanzo.eval_scores.
//
// There is deliberately NO AI-observations const here. This board and o11y.go
// once each named the observation table — one fact stated twice — and the twins
// drifted into pointing at a database that did not exist without either being
// noticed. The projection now lives ONCE in o11y.go (o11yAIObs = event.span plus
// the o11yGenAI* attribute consts; same package) and this file reads it there.
const (
	aimUsageTable = "hanzo.cloud_usage"
	aimEvalTraces = "hanzo.eval_traces"
	aimEvalScores = "hanzo.eval_scores"
	aimTopN       = 12
)

// aiMetrics is the whole AI-metrics board payload.
type aiMetrics struct {
	Range        string           `json:"range"`
	Start        string           `json:"start"`
	End          string           `json:"end"`
	O11yAI       aimO11yAI        `json:"o11yAi"`
	Usage        aimUsage         `json:"usage"`
	Evals        aimEvals         `json:"evals"`
	TopModels    []aimModelStat   `json:"topModels"`    // cloud_usage per-model (populated today)
	O11yAIModels []aimLfModelStat `json:"o11yAiModels"` // gen_ai spans per-model
	ScoreNames   []aimScoreStat   `json:"scoreNames"`   // eval_scores per score-name
	EvalRuns     []aimRunStat     `json:"evalRuns"`     // recent eval runs (progress)
	ScoreSeries  []aimScorePoint  `json:"scoreSeries"`  // avg eval score over time (progress trend)
}

// aimO11yAI is the fleet-wide LLM generation rollup over gen_ai spans.
// Cost is USD (the _o11y.gen_ai.total_cost attribute's native unit); latency is
// milliseconds (span duration is nanoseconds; rendered /1e6).
type aimO11yAI struct {
	Generations  int64   `json:"generations"`
	CostUsd      float64 `json:"costUsd"`
	LatencyMsAvg float64 `json:"latencyMsAvg"`
	LatencyMsP95 float64 `json:"latencyMsP95"`
}

// aimUsage is the fleet LLM-usage KPI band from the live cloud_usage ledger.
type aimUsage struct {
	Requests         int64 `json:"requests"`
	Tokens           int64 `json:"tokens"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	CostCents        int64 `json:"costCents"`
	Models           int64 `json:"models"`
}

// aimEvals is the fleet eval KPI band: the trace half (eval_traces) and the score
// half (eval_scores). LatencyMsAvg is the mean model-under-test call window.
type aimEvals struct {
	Runs         int64   `json:"runs"`
	Traces       int64   `json:"traces"`
	Datasets     int64   `json:"datasets"`
	Models       int64   `json:"models"`
	LatencyMsAvg float64 `json:"latencyMsAvg"`
	Scores       int64   `json:"scores"`
	ScoreNames   int64   `json:"scoreNames"`
	AvgScore     float64 `json:"avgScore"`
}

// aimModelStat is one row of the per-model usage leaderboard (cloud_usage).
type aimModelStat struct {
	Model     string `json:"model"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

// aimLfModelStat is one row of the per-model gen_ai-span leaderboard.
type aimLfModelStat struct {
	Model       string  `json:"model"`
	Generations int64   `json:"generations"`
	CostUsd     float64 `json:"costUsd"`
}

// aimScoreStat is one row of the per-score-name eval leaderboard (eval_scores).
type aimScoreStat struct {
	Name     string  `json:"name"`
	Count    int64   `json:"count"`
	AvgValue float64 `json:"avgValue"`
	MinValue float64 `json:"minValue"`
	MaxValue float64 `json:"maxValue"`
}

// aimRunStat is one recent eval run: its dataset, how many scores it recorded, its
// mean score, and when it last ran — the run-level eval-progress row.
type aimRunStat struct {
	RunName  string  `json:"runName"`
	Dataset  string  `json:"dataset"`
	Scores   int64   `json:"scores"`
	AvgValue float64 `json:"avgValue"`
	LastTs   string  `json:"lastTs"`
}

// aimScorePoint is one bucket of the avg-eval-score-over-time trend.
type aimScorePoint struct {
	Ts       string  `json:"ts"`
	AvgValue float64 `json:"avgValue"`
	Count    int64   `json:"count"`
}

// aimetrics is the fleet AI board: LLM generations over gen_ai spans (count, cost,
// avg/p95 latency, per-model), per-model usage from the live cloud_usage ledger, and
// the eval plane (traces, scores, score names, runs, and the average-score trend).
//
// Every signal degrades INDEPENDENTLY — a table that is absent or errors contributes its
// zero value and the read still succeeds. Generation latency is a SEPARATE query from
// generations and cost on purpose: a duration/attribute mismatch there must not zero
// the two numbers that did read.
//
// Example: {"range":"7d"}
// Response: {"status":"ok","msg":"","data":{"range":"7d","start":"2026-07-20T00:00:00Z",
// "end":"2026-07-27T00:00:00Z","topModels":[],"o11yAiModels":[],"scoreNames":[],
// "evalRuns":[],"scoreSeries":[]}}
func aimetrics(ctx context.Context, in *rangeIn) (*aimetricsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	rangeLabel := o11yRange(in.Range)
	since := computeSince(rangeLabel)
	payload := aiMetrics{
		Range:        rangeLabel,
		Start:        since.Format(time.RFC3339),
		End:          time.Now().UTC().Format(time.RFC3339),
		TopModels:    []aimModelStat{},
		O11yAIModels: []aimLfModelStat{},
		ScoreNames:   []aimScoreStat{},
		EvalRuns:     []aimRunStat{},
		ScoreSeries:  []aimScorePoint{},
	}

	// Honest-empty when the warehouse is not connected: the board renders its zero
	// state, never a fabricated fleet.
	if !datastore.Ready() {
		return &aimetricsOut{Status: core.OK, Data: &payload}, nil
	}

	sinceTS := chTS(since) // DateTime literal — cloud_usage.timestamp, span.time, eval_*.ts
	interval := o11yBucket(rangeLabel)

	// ── LLM generations (fleet) over gen_ai spans — totals builder SHARED with the
	// o11y board (o11y.go): one query, one builder, two boards ──
	if rows, err := datastore.Query(ctx, o11yLLMSQL(), sinceTS); err == nil {
		r := firstRowOr(rows)
		payload.O11yAI.Generations = chInt64(r["gens"])
		payload.O11yAI.CostUsd = chFloat64(r["cost"])
	}
	// Generation latency (separate query so a duration/attribute mismatch never
	// zeroes the proven generations+cost number above).
	if rows, err := datastore.Query(ctx, aimO11yAILatencySQL(), sinceTS); err == nil {
		r := firstRowOr(rows)
		payload.O11yAI.LatencyMsAvg = chFloat64(r["lat_avg"])
		payload.O11yAI.LatencyMsP95 = chFloat64(r["lat_p95"])
	}
	// Generations per-model.
	if rows, err := datastore.Query(ctx, aimO11yAIModelsSQL(), sinceTS); err == nil {
		payload.O11yAIModels = lfModelsFromRows(rows)
	}

	// ── Per-model usage (fleet) from the live cloud_usage ledger ──
	if rows, err := datastore.Query(ctx, aimUsageTotalsSQL(), sinceTS); err == nil {
		fillAimUsage(&payload.Usage, firstRowOr(rows))
	}
	if rows, err := datastore.Query(ctx, aimTopModelsSQL(), sinceTS); err == nil {
		payload.TopModels = aimModelsFromRows(rows)
	}

	// ── Evals (fleet): traces + scores + progress ──
	if rows, err := datastore.Query(ctx, aimEvalTracesSQL(), sinceTS); err == nil {
		fillAimEvalTraces(&payload.Evals, firstRowOr(rows))
	}
	if rows, err := datastore.Query(ctx, aimEvalScoresSQL(), sinceTS); err == nil {
		fillAimEvalScores(&payload.Evals, firstRowOr(rows))
	}
	if rows, err := datastore.Query(ctx, aimScoreNamesSQL(), sinceTS); err == nil {
		payload.ScoreNames = scoreNamesFromRows(rows)
	}
	if rows, err := datastore.Query(ctx, aimEvalRunsSQL(), sinceTS); err == nil {
		payload.EvalRuns = evalRunsFromRows(rows)
	}
	if rows, err := datastore.Query(ctx, aimScoreSeriesSQL(interval), sinceTS); err == nil {
		payload.ScoreSeries = scoreSeriesFromRows(rows)
	}

	return &aimetricsOut{Status: core.OK, Data: &payload}, nil
}

// aimetricsOut is the GET /v1/admin/aimetrics envelope.
type aimetricsOut struct {
	Status string     `json:"status"`
	Msg    string     `json:"msg"`
	Data   *aiMetrics `json:"data"`
}

// ── pure SQL builders (static SQL + one positional time bound; unit-tested) ──

// (Generation TOTALS come from o11yLLMSQL in o11y.go — the one shared builder.)

// aimO11yAILatencySQL projects generation latency from the span's own duration
// (UInt64 nanoseconds → ms). duration > 0 guards the unset/instant case the way
// end_time > start_time guarded the old two-column store.
func aimO11yAILatencySQL() string {
	lat := "(duration / 1e6)"
	return "SELECT round(avg(" + lat + "), 2) AS lat_avg, round(quantile(0.95)(" + lat + "), 2) AS lat_p95 " +
		"FROM " + o11yAIObs + " WHERE " + o11yGenAISpan + " AND time >= ? AND duration > 0"
}

func aimO11yAIModelsSQL() string {
	return "SELECT " + o11yGenAIModel + " AS model, count() AS gens, sum(" + o11yGenAICost + ") AS cost " +
		"FROM " + o11yAIObs + " WHERE " + o11yGenAISpan + " AND time >= ? AND model != '' " +
		"GROUP BY model ORDER BY gens DESC LIMIT " + strconv.Itoa(aimTopN)
}

func aimUsageTotalsSQL() string {
	return "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, uniqExact(model) AS models " +
		"FROM " + aimUsageTable + " WHERE timestamp >= ?"
}

func aimTopModelsSQL() string {
	return "SELECT model, count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + aimUsageTable +
		" WHERE timestamp >= ? AND model != '' GROUP BY model ORDER BY requests DESC LIMIT " + strconv.Itoa(aimTopN)
}

func aimEvalTracesSQL() string {
	lat := "(toUnixTimestamp64Milli(end_time) - toUnixTimestamp64Milli(start_time))"
	return "SELECT count() AS traces, uniqExact(run_name) AS runs, uniqExact(dataset) AS datasets, " +
		"uniqExact(model) AS models, round(avgIf(" + lat + ", end_time > start_time), 2) AS lat_avg " +
		"FROM " + aimEvalTraces + " WHERE ts >= ?"
}

func aimEvalScoresSQL() string {
	return "SELECT count() AS scores, round(avg(value), 4) AS avg_value, uniqExact(name) AS score_names " +
		"FROM " + aimEvalScores + " WHERE ts >= ?"
}

func aimScoreNamesSQL() string {
	return "SELECT name, count() AS n, round(avg(value), 4) AS avg_value, " +
		"round(min(value), 4) AS min_value, round(max(value), 4) AS max_value " +
		"FROM " + aimEvalScores + " WHERE ts >= ? AND name != '' GROUP BY name ORDER BY n DESC LIMIT " + strconv.Itoa(aimTopN)
}

func aimEvalRunsSQL() string {
	return "SELECT run_name, any(dataset) AS dataset, count() AS scores, round(avg(value), 4) AS avg_value, " +
		"max(ts) AS last_ts FROM " + aimEvalScores + " WHERE ts >= ? AND run_name != '' " +
		"GROUP BY run_name ORDER BY last_ts DESC LIMIT " + strconv.Itoa(aimTopN)
}

func aimScoreSeriesSQL(interval string) string {
	return "SELECT toStartOfInterval(ts, INTERVAL " + interval + ") AS ts, " +
		"round(avg(value), 4) AS avg_value, count() AS n FROM " + aimEvalScores +
		" WHERE ts >= ? GROUP BY ts ORDER BY ts"
}

// ── pure row parsers (unit-tested) ──

func fillAimUsage(u *aimUsage, r map[string]any) {
	u.Requests = chInt64(r["requests"])
	u.Tokens = chInt64(r["tokens"])
	u.PromptTokens = chInt64(r["prompt_tokens"])
	u.CompletionTokens = chInt64(r["completion_tokens"])
	u.CostCents = chInt64(r["cost_cents"])
	u.Models = chInt64(r["models"])
}

func fillAimEvalTraces(e *aimEvals, r map[string]any) {
	e.Traces = chInt64(r["traces"])
	e.Runs = chInt64(r["runs"])
	e.Datasets = chInt64(r["datasets"])
	e.Models = chInt64(r["models"])
	e.LatencyMsAvg = chFloat64(r["lat_avg"])
}

func fillAimEvalScores(e *aimEvals, r map[string]any) {
	e.Scores = chInt64(r["scores"])
	e.AvgScore = chFloat64(r["avg_value"])
	e.ScoreNames = chInt64(r["score_names"])
}

func aimModelsFromRows(rows []map[string]any) []aimModelStat {
	out := make([]aimModelStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimModelStat{
			Model:     chStr(r["model"]),
			Requests:  chInt64(r["requests"]),
			Tokens:    chInt64(r["tokens"]),
			CostCents: chInt64(r["cost_cents"]),
		})
	}
	return out
}

func lfModelsFromRows(rows []map[string]any) []aimLfModelStat {
	out := make([]aimLfModelStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimLfModelStat{
			Model:       chStr(r["model"]),
			Generations: chInt64(r["gens"]),
			CostUsd:     chFloat64(r["cost"]),
		})
	}
	return out
}

func scoreNamesFromRows(rows []map[string]any) []aimScoreStat {
	out := make([]aimScoreStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimScoreStat{
			Name:     chStr(r["name"]),
			Count:    chInt64(r["n"]),
			AvgValue: chFloat64(r["avg_value"]),
			MinValue: chFloat64(r["min_value"]),
			MaxValue: chFloat64(r["max_value"]),
		})
	}
	return out
}

func evalRunsFromRows(rows []map[string]any) []aimRunStat {
	out := make([]aimRunStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimRunStat{
			RunName:  chStr(r["run_name"]),
			Dataset:  chStr(r["dataset"]),
			Scores:   chInt64(r["scores"]),
			AvgValue: chFloat64(r["avg_value"]),
			LastTs:   chTime(r["last_ts"]),
		})
	}
	return out
}

func scoreSeriesFromRows(rows []map[string]any) []aimScorePoint {
	out := make([]aimScorePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimScorePoint{
			Ts:       chTime(r["ts"]),
			AvgValue: chFloat64(r["avg_value"]),
			Count:    chInt64(r["n"]),
		})
	}
	return out
}
