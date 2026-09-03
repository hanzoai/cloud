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
	"github.com/hanzoai/cloud/datastore"
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
	aimEvalTraces = "hanzo.eval_traces"
	aimEvalScores = "hanzo.eval_scores"
	aimTopN       = 12
)

// aiMetrics is the whole AI-metrics board payload.
type aiMetrics struct {
	// Range is the window every figure below covers: 24h, 7d or 30d. It is the
	// NORMALIZED value — an unrecognized ?range reads as 30d and says so here.
	Range string `json:"range"`
	// Start is the window's lower bound, RFC 3339 UTC, derived from Range.
	Start string `json:"start"`
	// End is the moment of this read, RFC 3339 UTC. The window is always right up
	// to now; there is no lag to allow for.
	End string `json:"end"`
	// O11yAI is the generation rollup over gen_ai spans. Its money is US DOLLARS,
	// unlike Usage below — the two halves of this board come from different planes
	// and each keeps its plane's unit.
	O11yAI aimO11yAI `json:"o11yAi"`
	// Usage is the fleet LLM-usage band from the ledger the gateway writes. Its
	// money is US CENTS.
	Usage aimUsage `json:"usage"`
	// Evals is the eval plane's band: the trace half and the score half.
	Evals aimEvals `json:"evals"`
	// TopModels is the twelve busiest models by request count, from the billing
	// ledger. Money in these rows is US cents.
	TopModels []aimModelStat `json:"topModels"`
	// TopActors is per-PRINCIPAL spend from the same ledger — whose bill it is,
	// which the per-model board cannot answer.
	TopActors []aimActorStat `json:"topActors"`
	// O11yAIModels is the twelve busiest models by GENERATION count, from gen_ai
	// spans. It answers the same question as TopModels from the other plane, and its
	// money is US dollars.
	O11yAIModels []aimLfModelStat `json:"o11yAiModels"`
	// ScoreNames is the twelve busiest scorers, each with its own distribution.
	ScoreNames []aimScoreStat `json:"scoreNames"`
	// EvalRuns is the twelve most RECENT eval runs, newest first — recency, not
	// quality.
	EvalRuns []aimRunStat `json:"evalRuns"`
	// ScoreSeries is the average eval score bucketed over the window, oldest first.
	// This series IS the training/eval progress signal on this board.
	ScoreSeries []aimScorePoint `json:"scoreSeries"`
}

// aimO11yAI is the fleet-wide LLM generation rollup over gen_ai spans.
// Cost is USD (the _o11y.gen_ai.total_cost attribute's native unit); latency is
// milliseconds (span duration is nanoseconds; rendered /1e6).
type aimO11yAI struct {
	// Generations is how many LLM calls the fleet made in the window — one per
	// gen_ai span.
	Generations int64 `json:"generations"`
	// CostUsd is their summed cost in US DOLLARS, not cents: it is the span
	// attribute's native unit and is carried through unconverted.
	CostUsd float64 `json:"costUsd"`
	// LatencyMsAvg is the mean generation duration in milliseconds, over spans that
	// recorded a duration at all.
	LatencyMsAvg float64 `json:"latencyMsAvg"`
	// LatencyMsP95 is the 95th-percentile generation duration in milliseconds — the
	// tail a user actually feels.
	LatencyMsP95 float64 `json:"latencyMsP95"`
}

// aimUsage is the fleet LLM-usage KPI band from the live cloud_usage ledger.
type aimUsage struct {
	// Requests is how many calls the ledger recorded in the window, errored ones
	// included.
	Requests int64 `json:"requests"`
	// Tokens is prompt plus completion across them.
	Tokens int64 `json:"tokens"`
	// PromptTokens is the input half of Tokens.
	PromptTokens int64 `json:"promptTokens"`
	// CompletionTokens is the generated half of Tokens.
	CompletionTokens int64 `json:"completionTokens"`
	// CostCents is what those calls cost, in US CENTS — this half of the board is
	// the billing ledger, so it is cents while the gen_ai half is dollars.
	CostCents int64 `json:"costCents"`
	// Models is how many DISTINCT models were called, not a count of calls.
	Models int64 `json:"models"`
}

// aimEvals is the fleet eval KPI band: the trace half (eval_traces) and the score
// half (eval_scores). LatencyMsAvg is the mean model-under-test call window.
type aimEvals struct {
	// Runs is how many DISTINCT eval runs left traces in the window.
	Runs int64 `json:"runs"`
	// Traces is how many eval traces were recorded — one per model-under-test call.
	Traces int64 `json:"traces"`
	// Datasets is how many distinct datasets those traces ran against.
	Datasets int64 `json:"datasets"`
	// Models is how many distinct models were under test.
	Models int64 `json:"models"`
	// LatencyMsAvg is the mean model-under-test call duration in milliseconds,
	// averaged only over traces whose end time is after their start — an unfinished
	// trace does not drag it down.
	LatencyMsAvg float64 `json:"latencyMsAvg"`
	// Scores is how many individual score values were recorded, across every
	// scorer.
	Scores int64 `json:"scores"`
	// ScoreNames is how many distinct scorers produced them.
	ScoreNames int64 `json:"scoreNames"`
	// AvgScore is the unweighted mean of every score value in the window,
	// regardless of scorer, to 4dp. Because the scorers are pooled it also moves
	// when the MIX of scorers changes, so read it as a trend and the per-scorer
	// rows for the level.
	AvgScore float64 `json:"avgScore"`
}

// aimModelStat is one row of the per-model usage leaderboard (cloud_usage).
type aimModelStat struct {
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

// aimActorStat is one row of the per-actor spend leaderboard (cloud_usage) — the
// answer to "whose spend is this", which the per-model board cannot give.
//
// Actor is the ledger's `user_id`, the "<org>/<sub>" the gateway recorded. A row
// naming an APPLICATION rather than a person is not hidden here: it is the signal
// worth reading, since an application is not somebody who can be asked about a
// bill. It is fleet-wide and therefore SuperAdmin-only, like every other figure on
// this board — the op's first line is core.Admit.
type aimActorStat struct {
	// Actor is the principal the ledger recorded, "<org>/<sub>". A value naming
	// an IAM APPLICATION rather than a person means that spend has no human
	// owner — which is the row to read first.
	Actor string `json:"actor"`
	// Requests is how many calls that principal made in the window.
	Requests int64 `json:"requests"`
	// Tokens is the total tokens those calls consumed.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost, in US cents, as the ledger recorded it.
	CostCents int64 `json:"costCents"`
}

// aimLfModelStat is one row of the per-model gen_ai-span leaderboard.
type aimLfModelStat struct {
	// Model is the model the span reported answering as, falling back to the model
	// it was asked for when the response named none.
	Model string `json:"model"`
	// Generations is how many gen_ai spans named it. The board ranks on this.
	Generations int64 `json:"generations"`
	// CostUsd is their summed cost in US DOLLARS — the span attribute's unit, not
	// the cents the ledger leaderboard beside this uses.
	CostUsd float64 `json:"costUsd"`
}

// aimScoreStat is one row of the per-score-name eval leaderboard (eval_scores).
type aimScoreStat struct {
	// Name is the scorer's name. Its scale is the scorer's own — there is no fleet
	// convention that these are 0..1 — so compare a name against itself over time,
	// never one name against another.
	Name string `json:"name"`
	// Count is how many values this scorer produced in the window. The board ranks
	// on this, so a busy scorer leads whether or not it scores well.
	Count int64 `json:"count"`
	// AvgValue is their mean, to 4dp.
	AvgValue float64 `json:"avgValue"`
	// MinValue is the lowest single value this scorer recorded in the window.
	MinValue float64 `json:"minValue"`
	// MaxValue is the highest. With Min it bounds the spread the mean hides.
	MaxValue float64 `json:"maxValue"`
}

// aimRunStat is one recent eval run: its dataset, how many scores it recorded, its
// mean score, and when it last ran — the run-level eval-progress row.
type aimRunStat struct {
	// RunName is the eval run's name, as the run recorded it. Unnamed runs are left
	// out.
	RunName string `json:"runName"`
	// Dataset is one dataset the run scored against — an arbitrary one of them if
	// the run spanned several, since a run is named per row here and a dataset is
	// not.
	Dataset string `json:"dataset"`
	// Scores is how many score values the run recorded in the window.
	Scores int64 `json:"scores"`
	// AvgValue is their mean, to 4dp, pooled across the run's scorers.
	AvgValue float64 `json:"avgValue"`
	// LastTs is when the run last recorded a score, RFC 3339 UTC. The board is
	// ordered by it, so these rows are the most RECENT runs, not the best ones.
	LastTs string `json:"lastTs"`
}

// aimScorePoint is one bucket of the avg-eval-score-over-time trend.
type aimScorePoint struct {
	// Ts is the bucket's start, RFC 3339 UTC. The bucket width follows the range —
	// an hour at 24h, six hours at 7d, a day at 30d — so it is not a fixed unit.
	Ts string `json:"ts"`
	// AvgValue is the mean of every score in that bucket, to 4dp. This series IS
	// the eval-progress signal.
	AvgValue float64 `json:"avgValue"`
	// Count is how many scores the mean was taken over — the weight behind the
	// point, and what tells a real dip from a thin bucket.
	Count int64 `json:"count"`
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
	rangeLabel := core.WarehouseRange(in.Range)
	since := core.WarehouseSince(rangeLabel)
	payload := aiMetrics{
		Range:        rangeLabel,
		Start:        since.Format(time.RFC3339),
		End:          time.Now().UTC().Format(time.RFC3339),
		TopModels:    []aimModelStat{},
		TopActors:    []aimActorStat{},
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

	sinceTS := core.CHTimeLit(since) // DateTime literal — cloud_usage.timestamp, span.time, eval_*.ts
	interval := o11yBucket(rangeLabel)
	fleet := ledgerScope{Since: since} // the ledger reads are fleet-wide here

	// ── LLM generations (fleet) over gen_ai spans — totals builder SHARED with the
	// o11y board (o11y.go): one query, one builder, two boards ──
	if rows, err := datastore.Query(ctx, o11yLLMSQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		payload.O11yAI.Generations = core.CHInt64(r["gens"])
		payload.O11yAI.CostUsd = core.CHFloat64(r["cost"])
	}
	// Generation latency (separate query so a duration/attribute mismatch never
	// zeroes the proven generations+cost number above).
	if rows, err := datastore.Query(ctx, aimO11yAILatencySQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		payload.O11yAI.LatencyMsAvg = core.CHFloat64(r["lat_avg"])
		payload.O11yAI.LatencyMsP95 = core.CHFloat64(r["lat_p95"])
	}
	// Generations per-model.
	if rows, err := datastore.Query(ctx, aimO11yAIModelsSQL(), sinceTS); err == nil {
		payload.O11yAIModels = lfModelsFromRows(rows)
	}

	// ── Per-model usage (fleet) from the live cloud_usage ledger (ledger.go) ──
	fillAimUsage(&payload.Usage, core.CHFirstRow(ledgerRows(ctx, ledgerTotals(fleet))))
	payload.TopModels = aimModelsFromRows(ledgerRows(ctx, ledgerByModel(fleet, aimTopN)))
	payload.TopActors = aimActorsFromRows(ledgerRows(ctx, ledgerByActor(fleet, aimTopN)))

	// ── Evals (fleet): traces + scores + progress ──
	if rows, err := datastore.Query(ctx, aimEvalTracesSQL(), sinceTS); err == nil {
		fillAimEvalTraces(&payload.Evals, core.CHFirstRow(rows))
	}
	if rows, err := datastore.Query(ctx, aimEvalScoresSQL(), sinceTS); err == nil {
		fillAimEvalScores(&payload.Evals, core.CHFirstRow(rows))
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
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the board. A warehouse that is not connected, or a table that is
	// missing, yields the ZERO board rather than a failure — so zeros here mean
	// either an idle fleet or a blind one, and this envelope cannot tell you which.
	Data *aiMetrics `json:"data"`
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
	u.Requests = core.CHInt64(r["requests"])
	u.Tokens = core.CHInt64(r["tokens"])
	u.PromptTokens = core.CHInt64(r["prompt_tokens"])
	u.CompletionTokens = core.CHInt64(r["completion_tokens"])
	u.CostCents = core.CHInt64(r["cost_cents"])
	u.Models = core.CHInt64(r["models"])
}

func fillAimEvalTraces(e *aimEvals, r map[string]any) {
	e.Traces = core.CHInt64(r["traces"])
	e.Runs = core.CHInt64(r["runs"])
	e.Datasets = core.CHInt64(r["datasets"])
	e.Models = core.CHInt64(r["models"])
	e.LatencyMsAvg = core.CHFloat64(r["lat_avg"])
}

func fillAimEvalScores(e *aimEvals, r map[string]any) {
	e.Scores = core.CHInt64(r["scores"])
	e.AvgScore = core.CHFloat64(r["avg_value"])
	e.ScoreNames = core.CHInt64(r["score_names"])
}

func aimModelsFromRows(rows []map[string]any) []aimModelStat {
	out := make([]aimModelStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimModelStat{
			Model:     core.CHStr(r["model"]),
			Requests:  core.CHInt64(r["requests"]),
			Tokens:    core.CHInt64(r["tokens"]),
			CostCents: core.CHInt64(r["cost_cents"]),
		})
	}
	return out
}

func aimActorsFromRows(rows []map[string]any) []aimActorStat {
	out := make([]aimActorStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimActorStat{
			Actor:     core.CHStr(r["actor"]),
			Requests:  core.CHInt64(r["requests"]),
			Tokens:    core.CHInt64(r["tokens"]),
			CostCents: core.CHInt64(r["cost_cents"]),
		})
	}
	return out
}

func lfModelsFromRows(rows []map[string]any) []aimLfModelStat {
	out := make([]aimLfModelStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimLfModelStat{
			Model:       core.CHStr(r["model"]),
			Generations: core.CHInt64(r["gens"]),
			CostUsd:     core.CHFloat64(r["cost"]),
		})
	}
	return out
}

func scoreNamesFromRows(rows []map[string]any) []aimScoreStat {
	out := make([]aimScoreStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimScoreStat{
			Name:     core.CHStr(r["name"]),
			Count:    core.CHInt64(r["n"]),
			AvgValue: core.CHFloat64(r["avg_value"]),
			MinValue: core.CHFloat64(r["min_value"]),
			MaxValue: core.CHFloat64(r["max_value"]),
		})
	}
	return out
}

func evalRunsFromRows(rows []map[string]any) []aimRunStat {
	out := make([]aimRunStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimRunStat{
			RunName:  core.CHStr(r["run_name"]),
			Dataset:  core.CHStr(r["dataset"]),
			Scores:   core.CHInt64(r["scores"]),
			AvgValue: core.CHFloat64(r["avg_value"]),
			LastTs:   core.CHTime(r["last_ts"]),
		})
	}
	return out
}

func scoreSeriesFromRows(rows []map[string]any) []aimScorePoint {
	out := make([]aimScorePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, aimScorePoint{
			Ts:       core.CHTime(r["ts"]),
			AvgValue: core.CHFloat64(r["avg_value"]),
			Count:    core.CHInt64(r["n"]),
		})
	}
	return out
}
