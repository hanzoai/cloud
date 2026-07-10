package contributor

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

// --- ComponentWeightsFromSBOM ---

func TestComponentWeightsFromSBOM_PrefersUsage(t *testing.T) {
	entries := []SBOMEntry{
		{Component: "@hanzo/bot", UsageCount: 30, TotalLines: 1000},
		{Component: "@hanzo/mcp", UsageCount: 70, TotalLines: 10},
	}
	w := ComponentWeightsFromSBOM(entries)
	if w["@hanzo/bot"] != 30 || w["@hanzo/mcp"] != 70 {
		t.Fatalf("expected usage weights {bot:30, mcp:70}, got %v", w)
	}
}

func TestComponentWeightsFromSBOM_FallsBackToLines(t *testing.T) {
	entries := []SBOMEntry{
		{Component: "@hanzo/bot", UsageCount: 0, TotalLines: 800},
		{Component: "@hanzo/mcp", UsageCount: 0, TotalLines: 200},
	}
	w := ComponentWeightsFromSBOM(entries)
	if w["@hanzo/bot"] != 800 || w["@hanzo/mcp"] != 200 {
		t.Fatalf("expected line weights {bot:800, mcp:200}, got %v", w)
	}
}

func TestComponentWeightsFromSBOM_FallsBackToEqual(t *testing.T) {
	entries := []SBOMEntry{
		{Component: "@hanzo/bot"},
		{Component: "@hanzo/mcp"},
	}
	w := ComponentWeightsFromSBOM(entries)
	if w["@hanzo/bot"] != 1 || w["@hanzo/mcp"] != 1 {
		t.Fatalf("expected equal weights {bot:1, mcp:1}, got %v", w)
	}
}

func TestComponentWeightsFromSBOM_SkipsEmptyComponent(t *testing.T) {
	entries := []SBOMEntry{{Component: "", UsageCount: 5}, {Component: "@hanzo/bot", UsageCount: 5}}
	w := ComponentWeightsFromSBOM(entries)
	if _, ok := w[""]; ok {
		t.Fatalf("empty component should be skipped, got %v", w)
	}
}

// --- DistributeRevenue ---

func TestDistributeRevenue_Proportional(t *testing.T) {
	out := DistributeRevenue(1000, map[string]int64{"a": 1, "b": 1})
	if out["a"] != 500 || out["b"] != 500 {
		t.Fatalf("expected {a:500,b:500}, got %v", out)
	}
}

func TestDistributeRevenue_RemainderSumsExactly(t *testing.T) {
	// 1000 / (1+2) → a=333, b=666, remainder 1 → goes to top weight (b).
	out := DistributeRevenue(1000, map[string]int64{"a": 1, "b": 2})
	var sum int64
	for _, v := range out {
		sum += v
	}
	if sum != 1000 {
		t.Fatalf("distributed sum=%d, want exactly 1000 (%v)", sum, out)
	}
	if out["b"] != 667 {
		t.Errorf("remainder should go to top-weight b: got b=%d (%v)", out["b"], out)
	}
}

func TestDistributeRevenue_EdgeCases(t *testing.T) {
	if got := DistributeRevenue(0, map[string]int64{"a": 1}); len(got) != 0 {
		t.Errorf("zero amount → empty, got %v", got)
	}
	if got := DistributeRevenue(100, map[string]int64{}); len(got) != 0 {
		t.Errorf("no weights → empty, got %v", got)
	}
	if got := DistributeRevenue(100, map[string]int64{"a": 0}); len(got) != 0 {
		t.Errorf("zero total weight → empty, got %v", got)
	}
}

// --- CalculatePayouts: the 25% revenue share ---

func TestCalculatePayouts_25PercentSplit(t *testing.T) {
	db := datastore.New(context.Background())

	// One component carries all revenue; two contributors split it 60/40.
	alice := New(db)
	alice.GitLogin = "alice"
	alice.Active = true
	alice.Verified = true
	alice.Attributions = []Attribution{{Component: "@hanzo/bot", Lines: 600, Percent: 60}}

	bob := New(db)
	bob.GitLogin = "bob"
	bob.Active = true
	bob.Verified = true
	bob.Attributions = []Attribution{{Component: "@hanzo/bot", Lines: 400, Percent: 40}}

	const total = int64(100000) // $1,000.00
	componentRevenue := map[string]int64{"@hanzo/bot": total}

	summary := CalculatePayouts(total, []Contributor{*alice, *bob}, componentRevenue, DefaultConfig())

	if summary.PlatformShare != 75000 {
		t.Errorf("PlatformShare=%d, want 75000 (75%%)", summary.PlatformShare)
	}
	if summary.ContributorPool != 25000 {
		t.Errorf("ContributorPool=%d, want 25000 (25%%)", summary.ContributorPool)
	}

	byLogin := map[string]int64{}
	var sum int64
	for _, a := range summary.Allocations {
		byLogin[a.GitLogin] = a.AmountCents
		sum += a.AmountCents
	}
	if sum != summary.ContributorPool {
		t.Errorf("allocations sum=%d, want full pool %d", sum, summary.ContributorPool)
	}
	if byLogin["alice"] != 15000 {
		t.Errorf("alice=%d, want 15000 (60%% of 25000)", byLogin["alice"])
	}
	if byLogin["bob"] != 10000 {
		t.Errorf("bob=%d, want 10000 (40%% of 25000)", byLogin["bob"])
	}
}

// TestCalculatePayouts_AttributionFromSBOM proves the end-to-end attribution:
// untagged revenue distributed by SBOM weight reaches contributors of those
// components.
func TestCalculatePayouts_AttributionFromSBOM(t *testing.T) {
	db := datastore.New(context.Background())

	// SBOM: bot used 3x, mcp 1x → 75%/25% of untagged revenue.
	entries := []SBOMEntry{
		{Component: "@hanzo/bot", UsageCount: 3},
		{Component: "@hanzo/mcp", UsageCount: 1},
	}
	weights := ComponentWeightsFromSBOM(entries)
	componentRevenue := DistributeRevenue(100000, weights) // $1,000 untagged

	if componentRevenue["@hanzo/bot"] != 75000 || componentRevenue["@hanzo/mcp"] != 25000 {
		t.Fatalf("attribution wrong: %v", componentRevenue)
	}

	carol := New(db)
	carol.GitLogin = "carol"
	carol.Active = true
	carol.Verified = true
	carol.Attributions = []Attribution{{Component: "@hanzo/mcp", Lines: 100, Percent: 100}}

	summary := CalculatePayouts(100000, []Contributor{*carol}, componentRevenue, DefaultConfig())
	if len(summary.Allocations) != 1 {
		t.Fatalf("want 1 allocation, got %d (%+v)", len(summary.Allocations), summary.Allocations)
	}
	if summary.Allocations[0].GitLogin != "carol" || summary.Allocations[0].AmountCents <= 0 {
		t.Errorf("carol should be paid for @hanzo/mcp, got %+v", summary.Allocations[0])
	}
}
