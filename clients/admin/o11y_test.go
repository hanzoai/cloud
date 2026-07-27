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

// TestO11yRange normalizes the enum and defaults to 30d.
func TestO11yRange(t *testing.T) {
	for in, want := range map[string]string{"24h": "24h", "7d": "7d", "30d": "30d", "": "30d", "bogus": "30d", " 7d ": "7d"} {
		if got := o11yRange(in); got != want {
			t.Errorf("o11yRange(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestO11yBucket maps each range to a fixed, injection-safe interval constant.
func TestO11yBucket(t *testing.T) {
	for r, want := range map[string]string{"24h": "1 HOUR", "7d": "6 HOUR", "30d": "1 DAY"} {
		if got := o11yBucket(r); got != want {
			t.Errorf("o11yBucket(%q) = %q, want %q", r, got, want)
		}
	}
}

// TestO11ySQL_ReadsCanonicalTables proves every fleet query reads the ONE
// datastore's canonical table, binds the time bound as a POSITIONAL param (one
// `?`), and never interpolates user input. The bucket interval is the only
// rendered value and it is a server-side constant.
func TestO11ySQL_ReadsCanonicalTables(t *testing.T) {
	cases := []struct {
		name, sql, table string
		wantQMarks       int
	}{
		{"usageTotals", o11yUsageTotalsSQL(), "hanzo.cloud_usage", 1},
		{"traceTotals", o11yTraceTotalsSQL(), "o11y_traces.distributed_spans", 1},
		{"logVolume", o11yLogVolumeSQL(), "o11y_logs.distributed_records", 1},
		{"usageSeries", o11yUsageSeriesSQL("1 HOUR"), "hanzo.cloud_usage", 1},
		{"logSeries", o11yLogSeriesSQL("1 HOUR"), "o11y_logs.distributed_records", 1},
		{"topOrgs", o11yTopOrgsSQL(), "hanzo.cloud_usage", 1},
		{"topModels", o11yTopModelsSQL(), "hanzo.cloud_usage", 1},
		{"topServices", o11yTopServicesSQL(), "o11y_traces.distributed_spans", 1},
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

// TestO11ySeriesSQL_IntervalBound proves the (constant) bucket interval is
// rendered into the series queries and grouped/ordered by the bucket.
func TestO11ySeriesSQL_IntervalBound(t *testing.T) {
	for _, iv := range []string{"1 HOUR", "6 HOUR", "1 DAY"} {
		u := o11yUsageSeriesSQL(iv)
		if !strings.Contains(u, "INTERVAL "+iv) || !strings.Contains(u, "GROUP BY ts ORDER BY ts") {
			t.Errorf("usage series must bucket by INTERVAL %s; got %q", iv, u)
		}
		l := o11yLogSeriesSQL(iv)
		if !strings.Contains(l, "INTERVAL "+iv) {
			t.Errorf("log series must bucket by INTERVAL %s; got %q", iv, l)
		}
	}
}

// TestO11yTop_LimitAndOrder proves the leaderboards bound + order the result.
func TestO11yTop_LimitAndOrder(t *testing.T) {
	if !strings.Contains(o11yTopOrgsSQL(), "ORDER BY requests DESC LIMIT 10") {
		t.Errorf("topOrgs must order by requests desc, limit %d", o11yTopN)
	}
	if !strings.Contains(o11yTopServicesSQL(), "LIMIT 12") {
		t.Errorf("topServices must limit %d", o11yServiceLimit)
	}
}

// TestO11ySpanSQL_ReadsRealColumns proves the RED lenses read the span plane's REAL
// columns, not the retired generation's compat ALIASes (durationNano / serviceName /
// hasError). The alias block goes with the dead generation, so a lens that still
// names one returns rows today and errors after the cut.
func TestO11ySpanSQL_ReadsRealColumns(t *testing.T) {
	for name, sql := range map[string]string{
		"traceTotals": o11yTraceTotalsSQL(),
		"topServices": o11yTopServicesSQL(),
	} {
		for _, dead := range []string{"durationNano", "serviceName", "hasError", "httpRoute"} {
			if strings.Contains(sql, dead) {
				t.Errorf("%s names the retired alias column %q; got %q", name, dead, sql)
			}
		}
		if !strings.Contains(sql, "duration_nano") {
			t.Errorf("%s must read duration_nano; got %q", name, sql)
		}
		if !strings.Contains(sql, spanService) {
			t.Errorf("%s must read %s; got %q", name, spanService, sql)
		}
	}
}

// TestFillUsageTotals reads the datastore row into the KPI band across the
// numeric variants the driver returns (uint64/int64/float64), honest zeros on
// an empty row.
func TestFillUsageTotals(t *testing.T) {
	var empty o11yTotals
	fillUsageTotals(&empty, map[string]any{})
	if empty.Requests != 0 || empty.Tokens != 0 || empty.Orgs != 0 {
		t.Fatalf("empty row must yield honest zeros; got %+v", empty)
	}
	var got o11yTotals
	fillUsageTotals(&got, map[string]any{
		"requests": uint64(274), "tokens": uint64(102597), "prompt_tokens": uint64(60000),
		"completion_tokens": uint64(42597), "cost_cents": uint64(216), "errors": uint64(41),
		"orgs": uint64(3), "models": uint64(42),
	})
	if got.Requests != 274 || got.Tokens != 102597 || got.CostCents != 216 || got.Orgs != 3 || got.Models != 42 || got.Errors != 41 {
		t.Fatalf("usage totals mis-parsed: %+v", got)
	}
}

// TestFillTraceTotals maps the RED metrics, including the float latency/error-rate
// columns (round()/quantile() land as float64; a Decimal-as-string is parsed).
func TestFillTraceTotals(t *testing.T) {
	var got o11yTotals
	fillTraceTotals(&got, map[string]any{
		"traces": uint64(4044354), "p50": float64(12.5), "p95": float64(340.2),
		"p99": "901.7", "err_rate": float64(1.25), "services": uint64(8),
	})
	if got.TraceCount != 4044354 || got.LatencyP50Ms != 12.5 || got.LatencyP95Ms != 340.2 {
		t.Fatalf("trace latency mis-parsed: %+v", got)
	}
	if got.LatencyP99Ms != 901.7 { // string→float64 path
		t.Errorf("p99 string→float64 = %v, want 901.7", got.LatencyP99Ms)
	}
	if got.TraceErrorRate != 1.25 || got.Services != 8 {
		t.Errorf("trace error/services mis-parsed: %+v", got)
	}
}

// TestTopParsers map datastore rows into the leaderboard view-models and preserve
// order (the SQL already ORDER BYs; the parser must not reorder or drop rows).
func TestTopParsers(t *testing.T) {
	orgs := topOrgsFromRows([]map[string]any{
		{"org": "hanzo", "requests": uint64(154), "tokens": uint64(38966), "cost_cents": uint64(114)},
		{"org": "maxpower", "requests": uint64(118), "tokens": uint64(61550), "cost_cents": uint64(101)},
	})
	if len(orgs) != 2 || orgs[0].Org != "hanzo" || orgs[1].Org != "maxpower" || orgs[0].Requests != 154 {
		t.Fatalf("top orgs mis-parsed/reordered: %+v", orgs)
	}
	svcs := topServicesFromRows([]map[string]any{
		{"service": "ingress", "requests": uint64(308125), "error_rate": float64(45.56), "p95": float64(38.5)},
		{"service": "gateway", "requests": uint64(1112), "error_rate": float64(0), "p95": float64(8.3)},
	})
	if len(svcs) != 2 || svcs[0].Service != "ingress" || svcs[0].ErrorRate != 45.56 || svcs[1].LatencyP95Ms != 8.3 {
		t.Fatalf("top services mis-parsed: %+v", svcs)
	}
	// Empty input → empty (non-nil) slice, never a panic.
	if got := usageSeriesFromRows(nil); got == nil || len(got) != 0 {
		t.Errorf("nil rows must yield empty slice, got %v", got)
	}
}

// TestChFloat64 covers the numeric coercions the float accessor must survive.
func TestChFloat64(t *testing.T) {
	cases := map[string]struct {
		in   any
		want float64
	}{
		"float64": {float64(3.14), 3.14},
		"float32": {float32(2.5), 2.5},
		"int64":   {int64(7), 7},
		"uint64":  {uint64(9), 9},
		"string":  {"12.5", 12.5},
		"badstr":  {"nope", 0},
		"nil":     {nil, 0},
	}
	for name, c := range cases {
		if got := chFloat64(c.in); got != c.want {
			t.Errorf("chFloat64(%s=%v) = %v, want %v", name, c.in, got, c.want)
		}
	}
}
