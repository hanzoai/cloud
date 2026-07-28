package usage

import (
	"testing"
	"time"
)

func TestCategoryOf(t *testing.T) {
	cases := map[string]string{
		"gpu-h100":       "GPU",
		"llm":            "LLM",
		"model:gpt-4o":   "LLM",
		"inference":      "LLM",
		"embedding":      "LLM",
		"machine":        "Compute",
		"compute":        "Compute",
		"cluster":        "Compute",
		"datastore":      "Storage",
		"vector":         "Storage",
		"s3":             "Storage",
		"agent":          "Agents",
		"bot":            "Agents",
		"":               "Uncategorized",
		"websearch":      "Websearch", // unmapped tag surfaces as its own titled bucket
		"search":         "Search",    // provisioning "search" kind → its own honest bucket, not mislabeled
		"gpu-h100,extra": "GPU",       // first tag wins
		"  LLM  ":        "LLM",       // trimmed + case-insensitive
	}
	for in, want := range cases {
		if got := categoryOf(in); got != want {
			t.Errorf("categoryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSpend(t *testing.T) {
	if !isSpend(ledgerTxn{Type: "withdraw", Amount: 100}) {
		t.Error("a positive withdraw is spend")
	}
	if isSpend(ledgerTxn{Type: "deposit", Amount: 100}) {
		t.Error("a deposit is NOT spend")
	}
	if isSpend(ledgerTxn{Type: "withdraw", Amount: -5}) {
		t.Error("a non-positive withdraw must never subtract from spend")
	}
	if isSpend(ledgerTxn{Type: "withdraw", Amount: 0}) {
		t.Error("a zero withdraw is not spend")
	}
}

func TestBuildSpend_WindowingCategoriesAndSeries(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) // 3-day window, day buckets
	roll := rollupWire{ConsumedCents: 5000, OverageCents: 100}
	roll.Balance.BalanceCents = 20000
	roll.Balance.AvailableCents = 15000

	txns := []ledgerTxn{
		{Type: "withdraw", Amount: 300, Tags: "gpu-h100", CreatedAt: "2026-07-01T06:00:00Z"},
		{Type: "withdraw", Amount: 200, Tags: "llm", CreatedAt: "2026-07-01T09:00:00Z"},
		{Type: "withdraw", Amount: 150, Tags: "gpu-h100", CreatedAt: "2026-07-03T10:00:00Z"},
		{Type: "deposit", Amount: 9999, Tags: "", CreatedAt: "2026-07-02T00:00:00Z"},    // credit — excluded
		{Type: "withdraw", Amount: 500, Tags: "llm", CreatedAt: "2026-06-30T23:59:59Z"}, // before window — excluded
		{Type: "withdraw", Amount: 700, Tags: "llm", CreatedAt: "2026-07-04T00:00:00Z"}, // == end (exclusive) — excluded
	}

	sp := buildSpend(true, roll, txns, start, end, "day")

	if !sp.Available {
		t.Fatal("Available must be true when commerce answered")
	}
	// Only the 3 in-window withdrawals count: 300+200+150 = 650.
	if sp.TotalCents != 650 {
		t.Fatalf("TotalCents = %d, want 650 (windowed withdrawals only)", sp.TotalCents)
	}
	if sp.MTDCents != 5000 || sp.OverageCents != 100 || sp.BalanceCents != 20000 || sp.AvailableCents != 15000 {
		t.Fatalf("rollup figures not carried: %+v", sp)
	}
	// Categories: GPU=450 (2 lines), LLM=200 (1 line), GPU first (larger).
	if len(sp.ByCategory) != 2 {
		t.Fatalf("ByCategory len = %d, want 2: %+v", len(sp.ByCategory), sp.ByCategory)
	}
	if sp.ByCategory[0].Category != "GPU" || sp.ByCategory[0].Cents != 450 || sp.ByCategory[0].Count != 2 {
		t.Fatalf("ByCategory[0] = %+v, want GPU/450/2", sp.ByCategory[0])
	}
	if sp.ByCategory[1].Category != "LLM" || sp.ByCategory[1].Cents != 200 || sp.ByCategory[1].Count != 1 {
		t.Fatalf("ByCategory[1] = %+v, want LLM/200/1", sp.ByCategory[1])
	}
	// Series: 3 day buckets, gap-filled. Day1=500, Day2=0, Day3=150.
	if len(sp.Series) != 3 {
		t.Fatalf("Series len = %d, want 3: %+v", len(sp.Series), sp.Series)
	}
	if sp.Series[0].Cents != 500 || sp.Series[1].Cents != 0 || sp.Series[2].Cents != 150 {
		t.Fatalf("Series cents = [%d %d %d], want [500 0 150]", sp.Series[0].Cents, sp.Series[1].Cents, sp.Series[2].Cents)
	}
}

func TestBuildSpend_HonestZerosWhenUnavailable(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	sp := buildSpend(false, rollupWire{ConsumedCents: 999}, []ledgerTxn{{Type: "withdraw", Amount: 100, CreatedAt: "2026-07-01T00:00:00Z"}}, start, end, "hour")
	if sp.Available {
		t.Fatal("Available must be false")
	}
	if sp.TotalCents != 0 || sp.MTDCents != 0 {
		t.Fatalf("unavailable spend must be honest zero, got total=%d mtd=%d", sp.TotalCents, sp.MTDCents)
	}
	// Slices must be non-nil (empty) so JSON emits [] not null.
	if sp.ByCategory == nil || sp.Series == nil {
		t.Fatalf("slices must be non-nil: byCategory=%v series=%v", sp.ByCategory, sp.Series)
	}
	if len(sp.ByCategory) != 0 || len(sp.Series) != 0 {
		t.Fatalf("unavailable spend must carry no rows, got %d cats %d points", len(sp.ByCategory), len(sp.Series))
	}
}

func TestBuildLLM(t *testing.T) {
	row := map[string]any{
		"requests":          uint64(12),
		"tokens":            uint64(3400),
		"prompt_tokens":     uint64(3000),
		"completion_tokens": uint64(400),
		"cost_cents":        uint64(87),
		"models":            uint64(3),
	}
	l := buildLLM(true, row)
	if !l.Available || l.Requests != 12 || l.Tokens != 3400 || l.PromptTokens != 3000 ||
		l.CompletionTokens != 400 || l.CostCents != 87 || l.Models != 3 {
		t.Fatalf("buildLLM mismatch: %+v", l)
	}
	// Unavailable → honest zeros, Available=false.
	z := buildLLM(false, row)
	if z.Available || z.Requests != 0 || z.Tokens != 0 || z.CostCents != 0 {
		t.Fatalf("unavailable LLM must be honest zero: %+v", z)
	}
}

func TestParseTxnTime(t *testing.T) {
	for _, s := range []string{"2026-07-01T06:00:00Z", "2026-07-01T06:00:00.123456Z", "2026-07-01 06:00:00", "2026-07-01"} {
		if parseTxnTime(s).IsZero() {
			t.Errorf("parseTxnTime(%q) should parse", s)
		}
	}
	if !parseTxnTime("not-a-time").IsZero() {
		t.Error("unparseable time must be zero (row dropped, never mis-bucketed)")
	}
}
