package finance

import (
	"math"
	"testing"
	"time"
)

// TestComputeFinance_Math is the PURE derivation proof: given a fixed multi-vendor COGS
// view and commerce revenue view, gross margin, margin %, runway, and profitability are
// exactly the arithmetic the dashboard promises — no I/O. The margin cost is the COGS
// total (all vendors); runway is the DO-credit projection.
func TestComputeFinance_Math(t *testing.T) {
	// COGS: $30k total across vendors. DO treasury: $40k credit, $10k left, burning
	// $1k/day. Revenue: $35k realized.
	in := FinanceInput{
		Cost: FinanceCost{
			Configured: true,
			TotalCents: 3_000_000, // $30,000 COGS across all vendors (the margin cost)
			DigitalOcean: DoCost{
				Configured:           true,
				CreditRemainingCents: 1_000_000, // $10,000 promo credit remaining
				AvgDailyBurnCents:    100_000,   // $1,000/day DO burn (runway input)
			},
		},
		Revenue: FinanceRevenue{
			Configured:        true,
			TotalRevenueCents: 3_500_000, // $35,000 revenue
			MRRCents:          500_000,   // $5,000 MRR
		},
	}
	got := ComputeFinance(in)

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

// TestComputeFinance_BurningFasterThanEarning proves the red state: cost exceeds revenue →
// negative margin, not profitable, runway still finite.
func TestComputeFinance_BurningFasterThanEarning(t *testing.T) {
	in := FinanceInput{
		Cost: FinanceCost{
			Configured: true,
			TotalCents: 4_000_000, // $40,000 COGS (the margin cost)
			DigitalOcean: DoCost{
				Configured:           true,
				CreditRemainingCents: 2_000_000, // $20,000 left
				AvgDailyBurnCents:    200_000,   // $2,000/day
			},
		},
		Revenue: FinanceRevenue{Configured: true, TotalRevenueCents: 1_000_000}, // $10,000
	}
	got := ComputeFinance(in)
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

// TestComputeFinance_HonestUnconfigured proves the DO-off path: no fabricated credit/burn,
// runway is NULL (not zero), margin is just revenue (cost 0), and margin % is 0 when
// revenue is 0.
func TestComputeFinance_HonestUnconfigured(t *testing.T) {
	in := FinanceInput{
		Cost:    FinanceCost{Configured: false, DigitalOcean: DoCost{Configured: false}}, // commerce + DO both off
		Revenue: FinanceRevenue{Configured: false},
	}
	got := ComputeFinance(in)
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

// TestComputeFinance_ZeroBurnNullRunway proves runway is null when DO is configured but
// burn is zero (no division by zero, no fabricated infinity).
func TestComputeFinance_ZeroBurnNullRunway(t *testing.T) {
	in := FinanceInput{
		Cost:    FinanceCost{Configured: true, TotalCents: 0, DigitalOcean: DoCost{Configured: true, CreditRemainingCents: 4_000_000, AvgDailyBurnCents: 0}},
		Revenue: FinanceRevenue{Configured: true, TotalRevenueCents: 100_000},
	}
	got := ComputeFinance(in)
	if got.Derived.RunwayDays != nil {
		t.Errorf("runwayDays must be nil when burn is 0, got %v", *got.Derived.RunwayDays)
	}
}

// TestAvgDailyBurn_ElapsedDays proves the run-rate is MTD spend over elapsed days (≥1), a
// real read over real time — never an invented rate.
func TestAvgDailyBurn_ElapsedDays(t *testing.T) {
	// $3,000 MTD on the 10th → $300/day.
	got := AvgDailyBurnCents(300_000, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	if got != 30_000 {
		t.Errorf("avgDailyBurn = %d, want 30000 ($300/day)", got)
	}
	// Day 1 must not divide by zero.
	if AvgDailyBurnCents(50_000, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) != 50_000 {
		t.Error("day-1 burn must be the full MTD spend (divide by 1)")
	}
}
