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
	// Mimics the direct datastore driver's native scan types (uint64 aggregates).
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

// TestBuildSeriesGapFill: sparse datastore buckets become an evenly-spaced,
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

// ── behavior lenses (topPages / topReferrers / topSources) ────────────────────

// TestBreakdownSQLBindsOrgPositionally: EVERY behavior lens inherits the tenancy
// invariant — the org is the trailing bound parameter (never interpolated), the
// predicate binds tenant_id, and the read is restricted to $pageview rows. The
// group key is a server-chosen constant (safe to interpolate) and the limit is a
// validated int. This is the SQL-boundary isolation proof for the new lenses.
func TestBreakdownSQLBindsOrgPositionally(t *testing.T) {
	start := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	const org = "o'; DROP TABLE hanzo.events; --" // hostile slug must NOT escape into SQL
	for _, keyExpr := range []string{pageKeyExpr, referrerKeyExpr, sourceKeyExpr} {
		sql, args := breakdownSQL(keyExpr, org, start, end, 10)
		if strings.Contains(sql, org) {
			t.Fatalf("org %q must NOT be interpolated into sql: %q", org, sql)
		}
		if !strings.Contains(sql, "tenant_id = ?") {
			t.Fatalf("breakdown must bind tenant_id positionally: %q", sql)
		}
		if !strings.Contains(sql, "timestamp >= ? AND timestamp < ?") {
			t.Fatalf("time bounds must be parameterized: %q", sql)
		}
		if len(args) != 3 {
			t.Fatalf("want 3 bound args (start,end,org), got %d: %v", len(args), args)
		}
		if got, ok := args[2].(string); !ok || got != org {
			t.Fatalf("org must be the trailing bound arg verbatim, want %q got %v", org, args[2])
		}
		if !strings.Contains(sql, "event = '$pageview'") {
			t.Fatalf("behavior lenses count only $pageview rows: %q", sql)
		}
		if !strings.Contains(sql, keyExpr) {
			t.Fatalf("breakdown must group by the key expr %q: %q", keyExpr, sql)
		}
		if !strings.Contains(sql, "LIMIT 10") {
			t.Fatalf("breakdown must bound cardinality by the validated limit: %q", sql)
		}
		// pct denominator is the TRUE in-window total (window fn), not the top-N sum.
		if !strings.Contains(sql, "sum(pageviews) OVER ()") {
			t.Fatalf("breakdown pct denominator must be the in-window pageview total: %q", sql)
		}
	}
}

// TestBreakdownBucketsDirectAndNone: the referrer lens buckets a missing or
// same-origin (self) referrer as "(direct)"; the source lens buckets an empty utm
// as "(none)". This is the origin/referral/campaign split done IN SQL.
func TestBreakdownBucketsDirectAndNone(t *testing.T) {
	start := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	refSQL, _ := breakdownSQL(referrerKeyExpr, "acme", start, end, 5)
	if !strings.Contains(refSQL, "'(direct)'") {
		t.Fatalf("referrer lens must bucket no/own referrer as (direct): %q", refSQL)
	}
	if !strings.Contains(refSQL, "domain(url)") {
		t.Fatalf("referrer lens must exclude same-origin (self) referrers via domain(url): %q", refSQL)
	}
	srcSQL, _ := breakdownSQL(sourceKeyExpr, "acme", start, end, 5)
	if !strings.Contains(srcSQL, "'(none)'") {
		t.Fatalf("source lens must bucket empty utm as (none): %q", srcSQL)
	}
}

// TestBuildBreakdownPctShareOfTotal: pct is each bucket's share of the in-window
// pageview TOTAL (the window-fn `total` column = 100), NOT of the returned-rows sum
// (80). So a top-N list honestly reflects the long tail. Values come through
// verbatim (key/pageviews/visitors).
func TestBuildBreakdownPctShareOfTotal(t *testing.T) {
	rows := []map[string]any{
		{"k": "/pricing", "pageviews": uint64(60), "visitors": uint64(40), "total": uint64(100)},
		{"k": "/docs", "pageviews": uint64(20), "visitors": uint64(15), "total": uint64(100)},
	}
	b := buildBreakdown(rows, true)
	if !b.Available || len(b.Items) != 2 {
		t.Fatalf("want 2 available items, got %+v", b)
	}
	if b.Items[0].Key != "/pricing" || b.Items[0].Pageviews != 60 || b.Items[0].Visitors != 40 {
		t.Fatalf("row 0 must carry values verbatim: %+v", b.Items[0])
	}
	// 60/100 = 60%, 20/100 = 20% — share of the in-window TOTAL, not the shown sum.
	if b.Items[0].Pct != 60 || b.Items[1].Pct != 20 {
		t.Fatalf("pct must be share of the in-window total: %v / %v", b.Items[0].Pct, b.Items[1].Pct)
	}
	if b.Source != "hanzo.events" {
		t.Fatalf("source want hanzo.events, got %q", b.Source)
	}
}

// TestBuildBreakdownHonestEmpty: an events-query failure (ok=false) yields an
// honestly-unavailable lens with an empty (non-nil) list — never fabricated rows,
// never a 500. Mirrors buildTopProducts's honest-empty exactly.
func TestBuildBreakdownHonestEmpty(t *testing.T) {
	b := buildBreakdown(nil, false)
	if b.Available {
		t.Fatal("breakdown must be unavailable when the events query failed")
	}
	if b.Items == nil || len(b.Items) != 0 {
		t.Fatalf("items must be an empty (non-nil) slice, got %#v", b.Items)
	}
	if b.Reason == "" || b.Source != "hanzo.events" {
		t.Fatalf("must carry honest reason + source, got %+v", b)
	}
}
