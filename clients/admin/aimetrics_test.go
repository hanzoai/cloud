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

import (
	"strings"
	"testing"
)

// TestAimSQL_ReadsCanonicalTables proves every AI-metrics query reads the ONE
// datastore's canonical table, binds the time bound as a POSITIONAL param (one
// `?`), and never interpolates user input. The bucket interval is the only rendered
// value in the series query and it is a server-side constant.
func TestAimSQL_ReadsCanonicalTables(t *testing.T) {
	cases := []struct {
		name, sql, table string
		wantQMarks       int
	}{
		{"usageTotals", aimUsageTotalsSQL(), "hanzo.cloud_usage", 1},
		{"topModels", aimTopModelsSQL(), "hanzo.cloud_usage", 1},
		{"evalTraces", aimEvalTracesSQL(), "hanzo.eval_traces", 1},
		{"evalScores", aimEvalScoresSQL(), "hanzo.eval_scores", 1},
		{"scoreNames", aimScoreNamesSQL(), "hanzo.eval_scores", 1},
		{"evalRuns", aimEvalRunsSQL(), "hanzo.eval_scores", 1},
		{"scoreSeries", aimScoreSeriesSQL("1 DAY"), "hanzo.eval_scores", 1},
	}
	for _, c := range cases {
		if !strings.Contains(c.sql, "FROM "+c.table) {
			t.Errorf("%s must read %s; got %q", c.name, c.table, c.sql)
		}
		if n := strings.Count(c.sql, "?"); n != c.wantQMarks {
			t.Errorf("%s: %d bind params, want %d (time bound only) — no interpolation; got %q", c.name, n, c.wantQMarks, c.sql)
		}
	}
}

// TestAimTop_LimitAndOrder proves the leaderboards bound + order the result.
func TestAimTop_LimitAndOrder(t *testing.T) {
	if !strings.Contains(aimTopModelsSQL(), "ORDER BY requests DESC LIMIT 12") {
		t.Errorf("topModels must order by requests desc, limit %d", aimTopN)
	}
	if !strings.Contains(aimScoreNamesSQL(), "GROUP BY name ORDER BY n DESC LIMIT 12") {
		t.Errorf("scoreNames must group+order+limit %d", aimTopN)
	}
	if !strings.Contains(aimEvalRunsSQL(), "ORDER BY last_ts DESC LIMIT 12") {
		t.Errorf("evalRuns must order by last_ts desc, limit %d", aimTopN)
	}
}

// TestAimScoreSeries_IntervalBound proves the (constant) bucket interval is
// rendered into the score-trend series query and grouped/ordered by the bucket.
func TestAimScoreSeries_IntervalBound(t *testing.T) {
	for _, iv := range []string{"1 HOUR", "6 HOUR", "1 DAY"} {
		s := aimScoreSeriesSQL(iv)
		if !strings.Contains(s, "INTERVAL "+iv) || !strings.Contains(s, "GROUP BY ts ORDER BY ts") {
			t.Errorf("score series must bucket by INTERVAL %s; got %q", iv, s)
		}
	}
}

// TestAimEvalLatencyGuarded proves the latency expressions guard end_time>start_time
// so a zero/default end_time never contributes a garbage (negative) latency.
func TestAimEvalLatencyGuarded(t *testing.T) {
	if !strings.Contains(aimEvalTracesSQL(), "end_time > start_time") {
		t.Errorf("eval traces latency must guard end_time>start_time; got %q", aimEvalTracesSQL())
	}
}

// TestFillAimUsage reads a cloud_usage row into the KPI band across the numeric
// variants the driver returns (uint64/int64/float64), honest zeros on an empty row.
func TestFillAimUsage(t *testing.T) {
	var empty aimUsage
	fillAimUsage(&empty, map[string]any{})
	if empty.Requests != 0 || empty.Tokens != 0 || empty.Models != 0 {
		t.Fatalf("empty row must yield honest zeros; got %+v", empty)
	}
	var got aimUsage
	fillAimUsage(&got, map[string]any{
		"requests": uint64(274), "tokens": uint64(102597), "prompt_tokens": uint64(60000),
		"completion_tokens": uint64(42597), "cost_cents": uint64(216), "models": uint64(42),
	})
	if got.Requests != 274 || got.Tokens != 102597 || got.CostCents != 216 || got.Models != 42 {
		t.Fatalf("usage totals mis-parsed: %+v", got)
	}
}

// TestFillAimEvals maps both eval halves (traces + scores) into the KPI band,
// including the float latency/score columns (round()/avg() land as float64; a
// Decimal-as-string is parsed).
func TestFillAimEvals(t *testing.T) {
	var e aimEvals
	fillAimEvalTraces(&e, map[string]any{
		"traces": uint64(1280), "runs": uint64(16), "datasets": uint64(4),
		"models": uint64(6), "lat_avg": float64(842.5),
	})
	fillAimEvalScores(&e, map[string]any{
		"scores": uint64(1280), "avg_value": "0.8125", "score_names": uint64(3),
	})
	if e.Traces != 1280 || e.Runs != 16 || e.Datasets != 4 || e.Models != 6 || e.LatencyMsAvg != 842.5 {
		t.Fatalf("eval traces mis-parsed: %+v", e)
	}
	if e.Scores != 1280 || e.ScoreNames != 3 || e.AvgScore != 0.8125 { // string→float64 path
		t.Fatalf("eval scores mis-parsed: %+v", e)
	}
}

// TestAimParsers map datastore rows into the view-models and preserve order (the
// SQL already ORDER BYs; a parser must not reorder or drop rows), with empty input
// yielding an empty (non-nil) slice rather than a panic.
func TestAimParsers(t *testing.T) {
	models := aimModelsFromRows([]map[string]any{
		{"model": "glm-5.2", "requests": uint64(154), "tokens": uint64(38966), "cost_cents": uint64(114)},
		{"model": "deepseek-v4-flash", "requests": uint64(118), "tokens": uint64(61550), "cost_cents": uint64(101)},
	})
	if len(models) != 2 || models[0].Model != "glm-5.2" || models[1].Model != "deepseek-v4-flash" || models[0].Requests != 154 {
		t.Fatalf("top models mis-parsed/reordered: %+v", models)
	}
	names := scoreNamesFromRows([]map[string]any{
		{"name": "accuracy", "n": uint64(320), "avg_value": float64(0.82), "min_value": float64(0), "max_value": float64(1)},
	})
	if len(names) != 1 || names[0].Name != "accuracy" || names[0].Count != 320 || names[0].AvgValue != 0.82 || names[0].MaxValue != 1 {
		t.Fatalf("score names mis-parsed: %+v", names)
	}
	runs := evalRunsFromRows([]map[string]any{
		{"run_name": "nightly-2026-07", "dataset": "gsm8k", "scores": uint64(200), "avg_value": float64(0.9), "last_ts": nil},
	})
	if len(runs) != 1 || runs[0].RunName != "nightly-2026-07" || runs[0].Dataset != "gsm8k" || runs[0].Scores != 200 || runs[0].AvgValue != 0.9 {
		t.Fatalf("eval runs mis-parsed: %+v", runs)
	}
	// Empty input → empty (non-nil) slices, never a panic.
	if got := scoreSeriesFromRows(nil); got == nil || len(got) != 0 {
		t.Errorf("nil rows must yield empty slice, got %v", got)
	}
	if got := aimModelsFromRows(nil); got == nil || len(got) != 0 {
		t.Errorf("nil rows must yield empty slice, got %v", got)
	}
}
