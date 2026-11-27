package usage

import (
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/decimal"

	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A line is named by what it metered — the same name /v1/billing/usage gives the row —
// never by the entry's ref, which is an act id.
func TestCategoryOf(t *testing.T) {
	cases := map[string]string{
		"desktop/visor": "Desktop sandbox",
		"session":       "Agent sessions",
		"op":            "Storage requests",
		"zen5":          "zen5",
		"":              "Uncategorized",
	}
	for in, want := range cases {
		if got := categoryOf(in); got != want {
			t.Errorf("categoryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSpend(t *testing.T) {
	if !isSpend(ledgerTxn{Kind: finance.KindUsage, Amount: 100}) {
		t.Error("a positive withdraw is spend")
	}
	if isSpend(ledgerTxn{Kind: finance.KindDeposit, Amount: 100}) {
		t.Error("a deposit is NOT spend")
	}
	if isSpend(ledgerTxn{Kind: finance.KindUsage, Amount: -5}) {
		t.Error("a non-positive withdraw must never subtract from spend")
	}
	if isSpend(ledgerTxn{Kind: finance.KindUsage, Amount: 0}) {
		t.Error("a zero withdraw is not spend")
	}
}

func TestBuildSpend_WindowingCategoriesAndSeries(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) // 3-day window, day buckets
	roll := rollup{ConsumedCents: 5000, BalanceCents: 20000}

	txns := []ledgerTxn{
		{Kind: finance.KindUsage, Amount: 300, Unit: "desktop/visor", CreatedAt: "2026-07-01T06:00:00Z"},
		{Kind: finance.KindUsage, Amount: 200, Unit: "zen5", CreatedAt: "2026-07-01T09:00:00Z"},
		{Kind: finance.KindUsage, Amount: 150, Unit: "desktop/visor", CreatedAt: "2026-07-03T10:00:00Z"},
		{Kind: finance.KindDeposit, Amount: 9999, Unit: "", CreatedAt: "2026-07-02T00:00:00Z"},  // credit — excluded
		{Kind: finance.KindUsage, Amount: 500, Unit: "zen5", CreatedAt: "2026-06-30T23:59:59Z"}, // before window — excluded
		{Kind: finance.KindUsage, Amount: 700, Unit: "zen5", CreatedAt: "2026-07-04T00:00:00Z"}, // == end (exclusive) — excluded
	}

	sp := buildSpend(true, roll, txns, start, end, "day")

	if !sp.Available {
		t.Fatal("Available must be true when commerce answered")
	}
	// Only the 3 in-window withdrawals count: 300+200+150 = 650.
	if sp.TotalCents != 650 {
		t.Fatalf("TotalCents = %d, want 650 (windowed withdrawals only)", sp.TotalCents)
	}
	// The wallet is ONE ledger number published under both names it has always
	// been published under. Overage is zero because a prepaid wallet has no
	// allowance to be beyond; the figure this replaces came from a JSON body that
	// never arrived in this binary.
	if sp.MTDCents != 5000 || sp.BalanceCents != 20000 || sp.AvailableCents != 20000 {
		t.Fatalf("ledger figures not carried: %+v", sp)
	}
	if sp.OverageCents != 0 {
		t.Fatalf("OverageCents = %d, want 0 — a prepaid wallet has no allowance to exceed", sp.OverageCents)
	}
	// Categories: Desktop sandbox=450 (2 lines), zen5=200 (1 line), larger first.
	if len(sp.ByCategory) != 2 {
		t.Fatalf("ByCategory len = %d, want 2: %+v", len(sp.ByCategory), sp.ByCategory)
	}
	if sp.ByCategory[0].Category != "Desktop sandbox" || sp.ByCategory[0].Cents != 450 || sp.ByCategory[0].Count != 2 {
		t.Fatalf("ByCategory[0] = %+v, want Desktop sandbox/450/2", sp.ByCategory[0])
	}
	if sp.ByCategory[1].Category != "zen5" || sp.ByCategory[1].Cents != 200 || sp.ByCategory[1].Count != 1 {
		t.Fatalf("ByCategory[1] = %+v, want zen5/200/1", sp.ByCategory[1])
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
	sp := buildSpend(false, rollup{ConsumedCents: 999}, []ledgerTxn{{Kind: finance.KindUsage, Amount: 100, CreatedAt: "2026-07-01T00:00:00Z"}}, start, end, "hour")
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
		"requests":           uint64(12),
		"tokens":             uint64(3400),
		"prompt_tokens":      uint64(3000),
		"completion_tokens":  uint64(400),
		"cost_cents":         uint64(87),
		"models":             uint64(3),
		"cache_read_tokens":  uint64(1200),
		"cache_write_tokens": uint64(300),
		"cache_saved_cents":  uint64(4),
	}
	l := buildLLM(true, row)
	if !l.Available || l.Requests != 12 || l.Tokens != 3400 || l.PromptTokens != 3000 ||
		l.CompletionTokens != 400 || l.CostCents != 87 || l.Models != 3 {
		t.Fatalf("buildLLM mismatch: %+v", l)
	}
	if l.CacheReadTokens != 1200 || l.CacheWriteTokens != 300 || l.CachedShare != 0.4 || l.CacheSavedCents != 4 {
		t.Fatalf("buildLLM cache mismatch: %+v", l)
	}
	// Unavailable → honest zeros, Available=false.
	z := buildLLM(false, row)
	if z.Available || z.Requests != 0 || z.Tokens != 0 || z.CostCents != 0 {
		t.Fatalf("unavailable LLM must be honest zero: %+v", z)
	}
}

// The share is the cache's part of the prompt: 0 with nothing prompted or nothing
// read, four decimals, and never past 1 however a provider counts.
func TestCachedShare(t *testing.T) {
	for _, c := range []struct {
		read, prompt int64
		want         float64
	}{
		{0, 0, 0}, {0, 100, 0}, {50, 0, 0}, {1, 3, 0.3333}, {2, 3, 0.6667}, {100, 100, 1}, {120, 100, 1},
	} {
		if got := cachedShare(c.read, c.prompt); got != c.want {
			t.Errorf("cachedShare(%d, %d) = %v, want %v", c.read, c.prompt, got, c.want)
		}
	}
}

// A row the ledger predates the cache columns for reads as zeros, and the summary
// still names every field: the JSON carries cache and kai keys whatever answered.
func TestBuildKai(t *testing.T) {
	k := buildKai(true, map[string]any{"decisions": uint64(7), "input_tokens": uint64(910), "cost_cents": uint64(2)})
	if !k.Available || k.Decisions != 7 || k.InputTokens != 910 || k.CostCents != 2 {
		t.Fatalf("buildKai mismatch: %+v", k)
	}
	if z := buildKai(false, map[string]any{"decisions": uint64(7)}); z != (Kai{}) {
		t.Fatalf("unavailable Kai must be honest zero: %+v", z)
	}
	if e := buildKai(true, nil); !e.Available || e.Decisions != 0 {
		t.Fatalf("an answering warehouse with no rows is zero decisions, available: %+v", e)
	}
	raw, err := json.Marshal(usageSummary{LLM: buildLLM(false, nil), Kai: buildKai(false, nil)})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"cacheReadTokens"`, `"cacheWriteTokens"`, `"cachedShare"`, `"cacheSavedCents"`, `"kai":{"available":false,"decisions":0,"inputTokens":0,"costCents":0}`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("summary JSON lacks %s: %s", key, raw)
		}
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

func TestBuildSpend_SumsSubCentRowsExactlyBeforeRounding(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	amt, err := decimal.Parse("0.004") // rounds to 0 cents individually
	if err != nil {
		t.Fatal(err)
	}
	txns := make([]ledgerTxn, 50) // 50 × $0.004 = $0.20 (20 cents)
	for i := range txns {
		txns[i] = ledgerTxn{
			Kind:      finance.KindUsage,
			Amount:    0,
			Exact:     amt,
			Unit:      "zen5-flash",
			CreatedAt: "2026-11-01T12:00:00Z",
		}
	}
	sp := buildSpend(true, rollup{ConsumedCents: 20, BalanceCents: 1500}, txns, start, end, "day")
	if sp.TotalCents != 20 {
		t.Fatalf("TotalCents = %d, want 20 (sub-cent rows must sum before rounding)", sp.TotalCents)
	}
	if len(sp.ByCategory) != 1 || sp.ByCategory[0].Cents != 20 || sp.ByCategory[0].Count != 50 {
		t.Fatalf("ByCategory = %+v, want zen5-flash/20/50", sp.ByCategory)
	}
	if maxLedgerRows < 10000 {
		t.Fatalf("maxLedgerRows = %d, want >= 10000 so 30d breakdown does not truncate below MTD", maxLedgerRows)
	}
}
