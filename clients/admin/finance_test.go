package admin

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestComputeFinance_Math is the PURE derivation proof: given a fixed multi-vendor
// COGS view and commerce revenue view, gross margin, margin %, runway, and
// profitability are exactly the arithmetic the dashboard promises — no I/O. The
// margin cost is the COGS total (all vendors); runway is the DO-credit projection.
func TestComputeFinance_Math(t *testing.T) {
	// COGS: $30k total across vendors. DO treasury: $40k credit, $10k left, burning
	// $1k/day. Revenue: $35k realized.
	in := financeInput{
		cost: financeCost{
			Configured: true,
			TotalCents: 3_000_000, // $30,000 COGS across all vendors (the margin cost)
			DigitalOcean: doCost{
				Configured:           true,
				CreditRemainingCents: 1_000_000, // $10,000 promo credit remaining
				AvgDailyBurnCents:    100_000,   // $1,000/day DO burn (runway input)
			},
		},
		revenue: financeRevenue{
			Configured:        true,
			TotalRevenueCents: 3_500_000, // $35,000 revenue
			MRRCents:          500_000,   // $5,000 MRR
		},
	}
	got := computeFinance(in)

	// margin = 35,000 - 30,000 = $5,000
	if got.Derived.GrossMarginCents != 500_000 {
		t.Errorf("grossMarginCents = %d, want 500000 ($5,000)", got.Derived.GrossMarginCents)
	}
	// marginPct = 5,000 / 35,000 * 100 = 14.2857…%
	if math.Abs(got.Derived.GrossMarginPct-14.285714) > 0.0001 {
		t.Errorf("grossMarginPct = %f, want ≈14.2857", got.Derived.GrossMarginPct)
	}
	// runway = 10,000 / 1,000 = 10 days
	if got.Derived.RunwayDays == nil || math.Abs(*got.Derived.RunwayDays-10) > 1e-9 {
		t.Errorf("runwayDays = %v, want 10", got.Derived.RunwayDays)
	}
	// revenue (35k) > cost (30k) → profitable this month.
	if !got.Derived.Profitable {
		t.Error("profitable must be true when revenue > cost")
	}
}

// TestComputeFinance_BurningFasterThanEarning proves the red state: cost exceeds
// revenue → negative margin, not profitable, runway still finite.
func TestComputeFinance_BurningFasterThanEarning(t *testing.T) {
	in := financeInput{
		cost: financeCost{
			Configured: true,
			TotalCents: 4_000_000, // $40,000 COGS (the margin cost)
			DigitalOcean: doCost{
				Configured:           true,
				CreditRemainingCents: 2_000_000, // $20,000 left
				AvgDailyBurnCents:    200_000,   // $2,000/day
			},
		},
		revenue: financeRevenue{Configured: true, TotalRevenueCents: 1_000_000}, // $10,000
	}
	got := computeFinance(in)
	if got.Derived.GrossMarginCents != -3_000_000 { // 10k - 40k = -30k
		t.Errorf("grossMarginCents = %d, want -3000000", got.Derived.GrossMarginCents)
	}
	if got.Derived.Profitable {
		t.Error("must NOT be profitable when cost > revenue")
	}
	// runway = 20,000 / 2,000 = 10 days
	if got.Derived.RunwayDays == nil || math.Abs(*got.Derived.RunwayDays-10) > 1e-9 {
		t.Errorf("runwayDays = %v, want 10", got.Derived.RunwayDays)
	}
}

// TestComputeFinance_HonestUnconfigured proves the DO-off path: no fabricated
// credit/burn, runway is NULL (not zero), margin is just revenue (cost 0), and
// margin % is 0 when revenue is 0.
func TestComputeFinance_HonestUnconfigured(t *testing.T) {
	in := financeInput{
		cost:    financeCost{Configured: false, DigitalOcean: doCost{Configured: false}}, // commerce + DO both off
		revenue: financeRevenue{Configured: false},
	}
	got := computeFinance(in)
	if got.Cost.DigitalOcean.Configured {
		t.Error("DO must report configured:false when the token is unset")
	}
	if got.Cost.DigitalOcean.CreditRemainingCents != 0 || got.Cost.DigitalOcean.AvgDailyBurnCents != 0 {
		t.Error("unconfigured DO must not fabricate credit/burn")
	}
	// runway is null (nil) — no honest runway without a burn rate.
	if got.Derived.RunwayDays != nil {
		t.Errorf("runwayDays must be nil when DO is unconfigured, got %v", *got.Derived.RunwayDays)
	}
	if got.Derived.GrossMarginPct != 0 {
		t.Errorf("grossMarginPct must be 0 when revenue is 0, got %f", got.Derived.GrossMarginPct)
	}
	// revenue 0 is not > cost 0 → not profitable.
	if got.Derived.Profitable {
		t.Error("zero revenue and zero cost is not profitable")
	}
}

// TestComputeFinance_ZeroBurnNullRunway proves runway is null when DO is
// configured but burn is zero (no division by zero, no fabricated infinity).
func TestComputeFinance_ZeroBurnNullRunway(t *testing.T) {
	in := financeInput{
		cost:    financeCost{Configured: true, TotalCents: 0, DigitalOcean: doCost{Configured: true, CreditRemainingCents: 4_000_000, AvgDailyBurnCents: 0}},
		revenue: financeRevenue{Configured: true, TotalRevenueCents: 100_000},
	}
	got := computeFinance(in)
	if got.Derived.RunwayDays != nil {
		t.Errorf("runwayDays must be nil when burn is 0, got %v", *got.Derived.RunwayDays)
	}
}

// TestAvgDailyBurn_ElapsedDays proves the run-rate is MTD spend over elapsed
// days (≥1), a real read over real time — never an invented rate.
func TestAvgDailyBurn_ElapsedDays(t *testing.T) {
	// $3,000 MTD on the 10th → $300/day.
	got := avgDailyBurnCents(300_000, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	if got != 30_000 {
		t.Errorf("avgDailyBurn = %d, want 30000 ($300/day)", got)
	}
	// Day 1 must not divide by zero.
	if avgDailyBurnCents(50_000, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) != 50_000 {
		t.Error("day-1 burn must be the full MTD spend (divide by 1)")
	}
}

// TestDollarsToCents proves the DO decimal-dollar → cents parsing, including the
// negative (credit) case and the blank fallback.
func TestDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"23.44", 2344},
		{"-40000.00", -4_000_000}, // promo credit held (negative account_balance)
		{"12.23", 1223},
		{"0", 0},
		{"", 0},
		{"  5.5 ", 550},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := dollarsToCents(c.in); got != c.want {
			t.Errorf("dollarsToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestMonthlyNormalizedCents proves annual/monthly normalization for MRR.
func TestMonthlyNormalizedCents(t *testing.T) {
	if got := monthlyNormalizedCents(12_000, "year"); got != 1_000 {
		t.Errorf("yearly $120 → monthly = %d, want 1000", got)
	}
	if got := monthlyNormalizedCents(2_000, "month"); got != 2_000 {
		t.Errorf("monthly must pass through, got %d", got)
	}
	if got := monthlyNormalizedCents(2_000, ""); got != 2_000 {
		t.Errorf("unknown interval must be treated as monthly, got %d", got)
	}
}

// newFakeDO serves the DO billing API with fixed decimal-dollar strings so the
// finance aggregation is deterministic. account_balance is NEGATIVE (credit held).
func newFakeDO() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/customers/my/balance"):
			// $10,000 credit remaining (account_balance = -10000.00), $3,000 MTD usage.
			io.WriteString(w, `{"month_to_date_balance":"-7000.00","account_balance":"-10000.00","month_to_date_usage":"3000.00","generated_at":"2026-07-15T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/customers/my/billing_history"):
			io.WriteString(w, `{"billing_history":[
				{"description":"Invoice for June 2026","amount":"2800.00","date":"2026-06-01T00:00:00Z","type":"Invoice","invoice_id":"1"},
				{"description":"Promo credit","amount":"-40000.00","date":"2026-05-01T00:00:00Z","type":"Credit","invoice_id":""}
			],"meta":{"total":2}}`)
		default:
			w.WriteHeader(404)
		}
	}))
}

// TestFinance_RealAggregation drives GET /v1/admin/finance against fake DO +
// commerce and proves the whole pipe: DO credit/spend/burn derived with the
// right sign, commerce revenue + MRR summed fleet-wide, and the derived margin/
// runway from computeFinance — all in one envelope.
func TestFinance_RealAggregation(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerceFinance()
	defer commerce.Close()
	do := newFakeDO()
	defer do.Close()

	doReq, s, _ := mountSvc(t, iam.server.URL, commerce.URL, "")
	s.do = newDOClientWithBase(do.URL, "test-do-token") // configured DO client
	admin := map[string]string{
		"X-User-IsAdmin": "true", "X-Org-Id": "admin",
		"Authorization": "Bearer operator-jwt", "Cookie": "iam_access_token=operator-jwt",
	}
	resp, body := doReq("GET", "/v1/admin/finance", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finance: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Status string      `json:"status"`
		Data   financeData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "ok" {
		t.Fatalf("finance status = %q, want ok", env.Status)
	}
	d := env.Data

	// ── COGS: commerce /v1/costs — DO compute $3,000 + OpenAI $500 = $3,500 total,
	// the multi-vendor breakdown that is now the margin cost (not the DO MTD spend).
	if !d.Cost.Configured {
		t.Fatal("commerce COGS must be configured in this test")
	}
	if d.Cost.TotalCents != 350_000 {
		t.Errorf("cost.totalCents = %d, want 350000 ($3,500 multi-vendor COGS)", d.Cost.TotalCents)
	}
	if len(d.Cost.Vendors) != 2 {
		t.Fatalf("cost.vendors must carry 2 lines (DO + OpenAI), got %d", len(d.Cost.Vendors))
	}
	if d.Cost.Vendors[0].Vendor == "" || d.Cost.Vendors[0].AmountCents == 0 {
		t.Errorf("vendor line must carry a vendor + amount, got %+v", d.Cost.Vendors[0])
	}

	// ── DO treasury: credit = -account_balance = $10,000; MTD usage $3,000 (runway
	// input, NOT the margin cost); burn-down history preserved.
	if !d.Cost.DigitalOcean.Configured {
		t.Fatal("DO must be configured in this test")
	}
	if d.Cost.DigitalOcean.CreditRemainingCents != 1_000_000 {
		t.Errorf("creditRemaining = %d, want 1000000 ($10,000 = -account_balance)", d.Cost.DigitalOcean.CreditRemainingCents)
	}
	if d.Cost.DigitalOcean.MonthToDateSpendCents != 300_000 {
		t.Errorf("monthToDateSpend = %d, want 300000 ($3,000)", d.Cost.DigitalOcean.MonthToDateSpendCents)
	}
	if d.Cost.DigitalOcean.AccountBalanceCents != -1_000_000 {
		t.Errorf("accountBalance = %d, want -1000000 (negative = credit held)", d.Cost.DigitalOcean.AccountBalanceCents)
	}
	if len(d.Cost.DigitalOcean.History) != 2 {
		t.Errorf("history must carry 2 entries, got %d", len(d.Cost.DigitalOcean.History))
	}

	// ── Commerce revenue: 2 orgs × $150 consumed = $300; MRR 2 × $50 = $100.
	if !d.Revenue.Configured {
		t.Fatal("commerce must be configured in this test")
	}
	if d.Revenue.TotalRevenueCents != 30_000 {
		t.Errorf("totalRevenue = %d, want 30000 (2 orgs × $150)", d.Revenue.TotalRevenueCents)
	}
	if d.Revenue.MRRCents != 10_000 {
		t.Errorf("MRR = %d, want 10000 (2 orgs × $50/mo active sub)", d.Revenue.MRRCents)
	}

	// ── Derived: margin = revenue 30,000 - COGS 350,000 = -320,000 (COGS > revenue).
	if d.Derived.GrossMarginCents != -320_000 {
		t.Errorf("grossMargin = %d, want -320000 (revenue 30k - COGS 350k)", d.Derived.GrossMarginCents)
	}
	if d.Derived.Profitable {
		t.Error("not profitable: revenue $300 < COGS $3,500")
	}
	// runway present because DO configured + burn > 0.
	if d.Derived.RunwayDays == nil {
		t.Error("runwayDays must be present when DO burn > 0")
	}
	// Every source reported (digitalocean + commerce both ok).
	src := map[string]sourceStatus{}
	for _, x := range d.Sources {
		src[x.Name] = x
	}
	if !src["digitalocean"].OK {
		t.Errorf("digitalocean source must be ok: %+v", src["digitalocean"])
	}
	if !src["commerce"].OK {
		t.Errorf("commerce source must be ok: %+v", src["commerce"])
	}
	if !src["commerce-costs"].OK {
		t.Errorf("commerce-costs source must be ok: %+v", src["commerce-costs"])
	}
}

// TestFinance_HonestUnconfiguredDO proves the ONE thing the user must provide:
// with no DO_API_TOKEN the endpoint returns cost.digitalocean = {configured:false},
// zero credit/burn, null runway — the honest state, never a fabricated $40k.
func TestFinance_HonestUnconfiguredDO(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerceFinance()
	defer commerce.Close()

	doReq, _, _ := mountSvc(t, iam.server.URL, commerce.URL, "") // s.do already has empty token → unconfigured
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := doReq("GET", "/v1/admin/finance", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finance: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data financeData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	// COGS still flows from commerce even with the DO treasury read off — the DO
	// compute COGS line belongs to commerce /v1/costs, decoupled from our DO credit
	// read, so a missing DO_API_TOKEN never blanks the margin.
	if !d.Cost.Configured || d.Cost.TotalCents == 0 || len(d.Cost.Vendors) == 0 {
		t.Errorf("commerce COGS must remain configured with vendors when DO treasury is off: %+v", d.Cost)
	}
	if d.Cost.DigitalOcean.Configured {
		t.Error("DO must report configured:false with no token")
	}
	if d.Cost.DigitalOcean.CreditRemainingCents != 0 || d.Cost.DigitalOcean.AvgDailyBurnCents != 0 {
		t.Error("unconfigured DO must not fabricate credit/burn")
	}
	if d.Cost.DigitalOcean.Error == "" {
		t.Error("unconfigured DO must carry an honest error string")
	}
	if d.Derived.RunwayDays != nil {
		t.Errorf("runway must be null when DO is unconfigured, got %v", *d.Derived.RunwayDays)
	}
	// History must be an empty array (renders as an empty chart), never nil/fabricated.
	if d.Cost.DigitalOcean.History == nil {
		t.Error("history must be [] (empty array), not null")
	}
	// Commerce still reports its real revenue even with DO off.
	if d.Revenue.TotalRevenueCents != 30_000 {
		t.Errorf("commerce revenue must still be real with DO off, got %d", d.Revenue.TotalRevenueCents)
	}
	// The digitalocean source must be present and NOT ok (honest not-configured).
	var doSrc *sourceStatus
	for i := range d.Sources {
		if d.Sources[i].Name == "digitalocean" {
			doSrc = &d.Sources[i]
		}
	}
	if doSrc == nil || doSrc.OK {
		t.Errorf("digitalocean source must be present and not-ok when unconfigured: %+v", doSrc)
	}
}

// TestFinance_RevenueSourceDown_NoFabrication proves the anti-fabrication property
// (RED MED-1): when the revenue source (IAM listOrgs) is unreadable but commerce
// COGS is fine, revenue reports configured:false (never a fake zero), so the board
// cannot render a fabricated negative margin / "burning" alarm. COGS flows on.
func TestFinance_RevenueSourceDown_NoFabrication(t *testing.T) {
	commerce := newFakeCommerceFinance()
	defer commerce.Close()

	// IAM points nowhere reachable → listOrgs errors; commerce /v1/costs still 200s.
	doReq, _, _ := mountSvc(t, "http://127.0.0.1:0", commerce.URL, "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := doReq("GET", "/v1/admin/finance", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finance: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data financeData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	// Revenue source unreadable → honest not-configured, NOT a fabricated zero.
	if d.Revenue.Configured {
		t.Error("revenue must report configured:false when the IAM org list is unreadable")
	}
	if d.Revenue.TotalRevenueCents != 0 {
		t.Errorf("unreadable revenue must be 0, got %d", d.Revenue.TotalRevenueCents)
	}
	// COGS is independent — still configured from commerce /v1/costs.
	if !d.Cost.Configured || d.Cost.TotalCents == 0 {
		t.Errorf("COGS must remain configured when the revenue source is down: %+v", d.Cost)
	}
	// The commerce (revenue) source is present and NOT ok — honest degraded state.
	var revSrc *sourceStatus
	for i := range d.Sources {
		if d.Sources[i].Name == "commerce" {
			revSrc = &d.Sources[i]
		}
	}
	if revSrc == nil || revSrc.OK {
		t.Errorf("commerce revenue source must be present and not-ok when unreadable: %+v", revSrc)
	}
}

// newFakeCommerceFinance serves the vendor-COGS god-view (/v1/costs) plus
// usage-rollup ($150 consumed) and subscriptions (one active $50/mo sub) so the
// finance COGS + revenue + MRR aggregation is deterministic.
func newFakeCommerceFinance() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/costs"):
			// The vendor-COGS god-view: DO compute $3,000 + OpenAI $500 = $3,500 total.
			io.WriteString(w, `{"period":"2026-07","vendors":[
				{"vendor":"digitalocean","service":"compute","amountCents":300000,"source":"actual","currency":"usd"},
				{"vendor":"openai","service":"llm-inference","amountCents":50000,"source":"actual","currency":"usd"}
			],"totalCents":350000,"currency":"usd"}`)
		case strings.HasSuffix(r.URL.Path, "/usage-rollup"):
			io.WriteString(w, `{"consumedCents":15000,"overageCents":0,"balance":{"balanceCents":0,"availableCents":0}}`)
		case strings.HasSuffix(r.URL.Path, "/subscriptions"):
			io.WriteString(w, `{"subscriptions":[
				{"status":"active","plan":{"price":5000,"currency":"usd","interval":"month"}},
				{"status":"canceled","plan":{"price":9900,"currency":"usd","interval":"month"}}
			]}`)
		default:
			w.WriteHeader(404)
		}
	}))
}
