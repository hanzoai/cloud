package costs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidPeriod(t *testing.T) {
	ok := []string{"2026-01", "2026-12", "2020-02"}
	bad := []string{"", "2026-13", "2026-00", "26-1", "2026-1", "2026/01", "2026-01-01", "abcd-ef"}
	for _, s := range ok {
		if !validPeriod(s) {
			t.Errorf("validPeriod(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validPeriod(s) {
			t.Errorf("validPeriod(%q) = true, want false", s)
		}
	}
}

func TestPeriodDefaultsToCurrentMonth(t *testing.T) {
	// A malformed period must NOT be trusted verbatim — it defaults to now.
	got := period("2026-13")
	if !validPeriod(got) {
		t.Fatalf("period(bad) = %q, not a valid YYYY-MM", got)
	}
	if period("2026-07") != "2026-07" {
		t.Errorf("period(valid) should pass through")
	}
}

func TestGrossMarginPct(t *testing.T) {
	cases := []struct {
		revenue, margin int64
		want            float64
	}{
		{0, 0, 0},       // no revenue -> 0, never NaN/Inf
		{0, -500, 0},    // no revenue, negative margin -> 0
		{1000, 500, 50}, // 50%
		{1000, 1000, 100},
		{1000, -200, -20}, // loss
		{300, 100, 33.33}, // rounding to 2dp
	}
	for _, c := range cases {
		if got := grossMarginPct(c.revenue, c.margin); got != c.want {
			t.Errorf("grossMarginPct(%d,%d) = %v, want %v", c.revenue, c.margin, got, c.want)
		}
	}
}

func TestUsdStringToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"12.34", 1234},
		{"1,234.56", 123456},
		{"100", 10000},
		{"0", 0},
		{"", 0},
		{"-5.00", 0}, // credits/negatives ignored as spend
		{"  9.99 ", 999},
		{"notanumber", 0},
	}
	for _, c := range cases {
		if got := usdStringToCents(c.in); got != c.want {
			t.Errorf("usdStringToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLookupCostRateLongestPrefix(t *testing.T) {
	// "gpt-4o-mini" must win over "gpt-4o" (longer prefix), and a dated suffix
	// must fall back to the base model.
	r, ok := lookupCostRate("gpt-4o-mini-2024-07-18")
	if !ok || r.InputCentsPerMTok != 15 {
		t.Errorf("gpt-4o-mini dated should map to mini rate 15, got %+v ok=%v", r, ok)
	}
	r, ok = lookupCostRate("gpt-4o-2024-08-06")
	if !ok || r.InputCentsPerMTok != 250 {
		t.Errorf("gpt-4o dated should map to 4o rate 250, got %+v ok=%v", r, ok)
	}
	if _, ok := lookupCostRate("totally-unknown-model"); ok {
		t.Errorf("unknown model must not match (honest, not guessed)")
	}
	if _, ok := lookupCostRate(""); ok {
		t.Errorf("empty model must not match")
	}
}

func TestModelCostCents(t *testing.T) {
	// gpt-4o: 250c/Mtok in, 1000c/Mtok out. 1M in + 1M out = 250 + 1000 = 1250c.
	got, ok := modelCostCents("gpt-4o", 1_000_000, 1_000_000)
	if !ok || got != 1250 {
		t.Errorf("modelCostCents gpt-4o 1M/1M = %d ok=%v, want 1250 true", got, ok)
	}
	// Unknown model -> honest (0,false), never a fabricated cost.
	if got, ok := modelCostCents("mystery", 5_000_000, 5_000_000); ok || got != 0 {
		t.Errorf("unknown model = %d ok=%v, want 0 false", got, ok)
	}
}

func TestAsInt64FromJSONFloat(t *testing.T) {
	// Metadata numerics arrive as float64 (JSON) — a naive int64 assert misses.
	if asInt64(float64(4200)) != 4200 {
		t.Errorf("asInt64(float64) failed")
	}
	if asInt64(int(7)) != 7 || asInt64(int64(9)) != 9 {
		t.Errorf("asInt64(int/int64) failed")
	}
	if asInt64("nope") != 0 || asInt64(nil) != 0 {
		t.Errorf("asInt64(bad) should be 0")
	}
}

func TestNormalizeProvider(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{"OpenAI", "", "openai"},
		{"", "gpt-4o", "openai"},
		{"", "claude-3-5-sonnet", "anthropic"},
		{"", "llama-v3p1-70b", "fireworks"},
		{"", "llama3-8b", "digitalocean"},
		{"fireworks-ai", "", "fireworks"},
		{"", "mystery-model", "other"},
	}
	for _, c := range cases {
		if got := normalizeProvider(c.provider, c.model); got != c.want {
			t.Errorf("normalizeProvider(%q,%q) = %q, want %q", c.provider, c.model, got, c.want)
		}
	}
}

// TestDigitalOceanCostNoToken: with no DO token the line is honest estimated, not
// a fabricated actual figure, and the report never fails.
func TestDigitalOceanCostNoToken(t *testing.T) {
	t.Setenv("DO_API_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	line := digitalOceanCost(context.Background(), &http.Client{}, "2026-07")
	if line.Source != SourceEstimated {
		t.Errorf("no-token DO line source = %q, want estimated", line.Source)
	}
	if line.AmountCents != 0 {
		t.Errorf("no-token DO amount = %d, want 0 (never fabricated)", line.AmountCents)
	}
	if line.Note == "" {
		t.Errorf("no-token DO line must carry an honest note")
	}
}

// TestDigitalOceanCostActual: with a stubbed DO billing API the invoice rows in
// the period sum to the actual amount and the source is actual. The token is sent
// as a Bearer header (asserted) and never in the URL.
func TestDigitalOceanCostActual(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"billing_history": []map[string]any{
				{"type": "Invoice", "amount": "120.50", "date": "2026-07-15T00:00:00Z"},
				{"type": "Invoice", "amount": "30.00", "date": "2026-07-28T00:00:00Z"},
				{"type": "Payment", "amount": "150.00", "date": "2026-07-16T00:00:00Z"}, // ignored
				{"type": "Invoice", "amount": "999.00", "date": "2026-06-30T23:59:59Z"}, // out of period
			},
		})
	}))
	defer srv.Close()

	// Point the DO client at the stub by swapping the base via a package-var seam.
	old := doAPIBaseOverride
	doAPIBaseOverride = srv.URL
	defer func() { doAPIBaseOverride = old }()

	t.Setenv("DO_API_TOKEN", "dop_v1_secrettoken")
	line := digitalOceanCost(context.Background(), srv.Client(), "2026-07")

	if line.Source != SourceActual {
		t.Fatalf("actual DO line source = %q, want actual (note=%q)", line.Source, line.Note)
	}
	// 120.50 + 30.00 = 150.50 -> 15050 cents.
	if line.AmountCents != 15050 {
		t.Errorf("actual DO amount = %d, want 15050", line.AmountCents)
	}
	if sawAuth != "Bearer dop_v1_secrettoken" {
		t.Errorf("DO token must be sent as Bearer header, got %q", sawAuth)
	}
}

// TestOpenAICostFallsBackToEstimate: no key -> the ledger estimate is used with an
// honest estimated source.
func TestOpenAICostFallsBackToEstimate(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	line := openAICost(context.Background(), &http.Client{}, "2026-07", 4200)
	if line.Source != SourceEstimated || line.AmountCents != 4200 {
		t.Errorf("no-key OpenAI = %+v, want estimated 4200", line)
	}
}

// TestOpenAICostActual: with a stubbed org-costs API the daily buckets sum to the
// actual amount and source is actual.
func TestOpenAICostActual(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(openaiCostsResponse{
			Data: []struct {
				StartTime int64 `json:"start_time"`
				EndTime   int64 `json:"end_time"`
				Results   []struct {
					Amount struct {
						Value    float64 `json:"value"`
						Currency string  `json:"currency"`
					} `json:"amount"`
				} `json:"results"`
			}{
				{StartTime: mustUnix("2026-07-05"), Results: mkResults(12.00)},
				{StartTime: mustUnix("2026-07-20"), Results: mkResults(8.34)},
			},
		})
	}))
	defer srv.Close()

	old := openaiAPIBaseOverride
	openaiAPIBaseOverride = srv.URL
	defer func() { openaiAPIBaseOverride = old }()

	t.Setenv("OPENAI_API_KEY", "sk-secret")
	line := openAICost(context.Background(), srv.Client(), "2026-07", 999)
	if line.Source != SourceActual {
		t.Fatalf("actual OpenAI source = %q want actual (note=%q)", line.Source, line.Note)
	}
	// 12.00 + 8.34 = 20.34 -> 2034 cents (NOT the 999 estimate).
	if line.AmountCents != 2034 {
		t.Errorf("actual OpenAI amount = %d, want 2034", line.AmountCents)
	}
}

func TestPendingVendorsHonestZero(t *testing.T) {
	vs := pendingVendors("2026-07")
	if len(vs) != len(pendingRegistry) {
		t.Fatalf("pending vendor count = %d, want %d", len(vs), len(pendingRegistry))
	}
	for _, v := range vs {
		if v.AmountCents != 0 || v.Source != SourceEstimated || v.Note == "" {
			t.Errorf("pending vendor %q must be estimated-0 with a note, got %+v", v.Vendor, v)
		}
	}
}

func TestTotal(t *testing.T) {
	vs := []VendorCost{{AmountCents: 100}, {AmountCents: 250}, {AmountCents: 0}}
	if got := total(vs); got != 350 {
		t.Errorf("total = %d, want 350", got)
	}
}

// ---- test helpers ----

func mkResults(v float64) []struct {
	Amount struct {
		Value    float64 `json:"value"`
		Currency string  `json:"currency"`
	} `json:"amount"`
} {
	r := struct {
		Amount struct {
			Value    float64 `json:"value"`
			Currency string  `json:"currency"`
		} `json:"amount"`
	}{}
	r.Amount.Value = v
	r.Amount.Currency = "usd"
	return []struct {
		Amount struct {
			Value    float64 `json:"value"`
			Currency string  `json:"currency"`
		} `json:"amount"`
	}{r}
}

func mustUnix(day string) int64 {
	t, _ := parseDay(day)
	return t
}
