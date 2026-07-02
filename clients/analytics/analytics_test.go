// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"strings"
	"testing"
	"time"
)

// TestLLMWhereBindsOrgPositionally is THE tenant-isolation proof at the SQL
// boundary: the org is ALWAYS the trailing bound parameter (never interpolated),
// the predicate is "organization = ?", and the org value NEVER appears in the SQL
// string. So a maxpower query and an acme query differ ONLY in a bound arg — one
// tenant can never read another's rows, and a hostile org slug can't escape into
// SQL.
func TestLLMWhereBindsOrgPositionally(t *testing.T) {
	start := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	for _, org := range []string{"maxpower", "acme", "o'; DROP TABLE hanzo.cloud_usage; --"} {
		sql, args := llmWhere(org, start, end)
		if !strings.Contains(sql, "organization = ?") {
			t.Fatalf("llmWhere sql must bind organization: %q", sql)
		}
		if strings.Contains(sql, org) {
			t.Fatalf("org %q must NOT be interpolated into sql: %q", org, sql)
		}
		if len(args) != 3 {
			t.Fatalf("want 3 bound args (start,end,org), got %d: %v", len(args), args)
		}
		if got, ok := args[2].(string); !ok || got != org {
			t.Fatalf("org must be the trailing bound arg verbatim, want %q got %v", org, args[2])
		}
		// Time bounds are also bound (as CH DateTime literals), never interpolated.
		if !strings.Contains(sql, "timestamp >= ? AND timestamp < ?") {
			t.Fatalf("time bounds must be parameterized: %q", sql)
		}
	}
}

// TestEventsWhereBindsOrgPositionally: the events lens keys on tenant_id, same
// bound-parameter discipline.
func TestEventsWhereBindsOrgPositionally(t *testing.T) {
	start := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	sql, args := eventsWhere("maxpower", start, end)
	if !strings.Contains(sql, "tenant_id = ?") {
		t.Fatalf("eventsWhere must bind tenant_id: %q", sql)
	}
	if strings.Contains(sql, "maxpower") {
		t.Fatalf("org must not be interpolated: %q", sql)
	}
	if got, ok := args[2].(string); !ok || got != "maxpower" {
		t.Fatalf("org must be trailing bound arg, got %v", args[2])
	}
}

// TestBuildLLMOverviewRealNumbers: the flagship assembler over a realistic row
// (maxpower's live shape ≈ 21 req / 3.2K tokens / $1.20 / 3 models). Proves the
// KPIs and the errorRate math are exact.
func TestBuildLLMOverviewRealNumbers(t *testing.T) {
	// Mimics the direct ClickHouse driver's native scan types (uint64 aggregates).
	row := map[string]any{
		"requests":          uint64(21),
		"tokens":            uint64(3200),
		"prompt_tokens":     uint64(2100),
		"completion_tokens": uint64(1100),
		"cost_cents":        uint64(120),
		"models":            uint64(3),
		"providers":         uint64(2),
		"errors":            uint64(0),
	}
	o := buildLLMOverview(row)
	if !o.Available {
		t.Fatal("llm overview must be available when the datastore answered")
	}
	if o.Requests != 21 || o.Tokens != 3200 || o.SpendCents != 120 || o.Models != 3 || o.Providers != 2 {
		t.Fatalf("KPI mismatch: %+v", o)
	}
	if o.PromptTokens != 2100 || o.CompletionTokens != 1100 {
		t.Fatalf("token split mismatch: %+v", o)
	}
	if o.ErrorRate != 0 {
		t.Fatalf("errorRate want 0, got %v", o.ErrorRate)
	}
	if o.Source != "hanzo.cloud_usage" {
		t.Fatalf("source want hanzo.cloud_usage, got %q", o.Source)
	}
}

// TestBuildLLMOverviewHonestEmpty: an empty aggregate (no usage in the window)
// yields honest zeros — never fabricated, and NOT unavailable (the datastore did
// answer; there is just nothing).
func TestBuildLLMOverviewHonestEmpty(t *testing.T) {
	o := buildLLMOverview(map[string]any{})
	if !o.Available {
		t.Fatal("empty window must still be Available (honest-zero, not unavailable)")
	}
	if o.Requests != 0 || o.Tokens != 0 || o.SpendCents != 0 || o.Models != 0 || o.ErrorRate != 0 {
		t.Fatalf("empty overview must be all-zero, got %+v", o)
	}
}

// TestErrorRate: errors/requests, rounded to 3 places.
func TestErrorRate(t *testing.T) {
	o := buildLLMOverview(map[string]any{"requests": uint64(10), "errors": uint64(2)})
	if o.ErrorRate != 0.2 {
		t.Fatalf("errorRate want 0.2, got %v", o.ErrorRate)
	}
}

// TestOrgAOverviewDiffersFromOrgB: combined with the where-isolation proof, each
// org's query returns only its own rows; distinct rows assemble to distinct
// overviews. (org A's overview != org B's.)
func TestOrgAOverviewDiffersFromOrgB(t *testing.T) {
	a := buildLLMOverview(map[string]any{"requests": uint64(21), "tokens": uint64(3200), "cost_cents": uint64(120)})
	b := buildLLMOverview(map[string]any{"requests": uint64(4), "tokens": uint64(500), "cost_cents": uint64(9)})
	if a.Requests == b.Requests || a.Tokens == b.Tokens || a.SpendCents == b.SpendCents {
		t.Fatalf("distinct orgs must assemble distinct overviews: a=%+v b=%+v", a, b)
	}
}

// TestBuildSeriesGapFill: sparse ClickHouse buckets become an evenly-spaced,
// gap-filled series across the window (zeros where no data, real where present).
func TestBuildSeriesGapFill(t *testing.T) {
	start := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) // 3 daily buckets: 28, 29, 30
	rows := []map[string]any{
		{"bucket": time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC), "requests": uint64(5), "tokens": uint64(100), "cost_cents": uint64(10)},
	}
	series := buildSeries(start, end, "day", rows)
	if len(series) != 3 {
		t.Fatalf("want 3 gap-filled daily points, got %d: %+v", len(series), series)
	}
	if series[0].Requests != 0 || series[0].T != "2026-06-28T00:00:00Z" {
		t.Fatalf("first bucket must be honest-zero 06-28, got %+v", series[0])
	}
	if series[1].Requests != 5 || series[1].Tokens != 100 || series[1].SpendCents != 10 {
		t.Fatalf("06-29 bucket must carry real data, got %+v", series[1])
	}
	if series[2].Requests != 0 {
		t.Fatalf("06-30 bucket must be honest-zero, got %+v", series[2])
	}
}

// TestBuildTopModelsSortAndPct: models sort by spend desc and each pct is its
// share of total spend.
func TestBuildTopModelsSortAndPct(t *testing.T) {
	rows := []map[string]any{
		{"model": "gpt-4o-mini", "provider": "do-ai", "requests": uint64(3), "tokens": uint64(200), "cost_cents": uint64(20)},
		{"model": "claude-sonnet-4-5", "provider": "anthropic", "requests": uint64(9), "tokens": uint64(3000), "cost_cents": uint64(80)},
	}
	top := buildTopModels(rows)
	if !top.Available || len(top.Items) != 2 {
		t.Fatalf("want 2 models available, got %+v", top)
	}
	if top.Items[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("highest-spend model must sort first, got %q", top.Items[0].Model)
	}
	// 80 of 100 total = 80%, 20 of 100 = 20%.
	if top.Items[0].Pct != 80 || top.Items[1].Pct != 20 {
		t.Fatalf("pct shares wrong: %v / %v", top.Items[0].Pct, top.Items[1].Pct)
	}
}

// TestBuildTopProductsHonestEmpty: when the events table is absent (ok=false) the
// products lens is honestly reported unavailable with an empty (non-nil) list.
func TestBuildTopProductsHonestEmpty(t *testing.T) {
	tp := buildTopProducts(nil, false)
	if tp.Available {
		t.Fatal("products must be unavailable when events table is absent")
	}
	if tp.Items == nil || len(tp.Items) != 0 {
		t.Fatalf("items must be an empty (non-nil) slice, got %#v", tp.Items)
	}
	if tp.Reason == "" || tp.Source != "hanzo.events" {
		t.Fatalf("must carry honest reason + source, got %+v", tp)
	}
}
