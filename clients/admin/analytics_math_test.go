package admin

import (
	"math"
	"testing"
	"time"
)

// mkTime is a test helper for an RFC3339-ish instant.
func mkTime(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// fleetFixture builds a deterministic 3-customer fleet with known signup cohorts
// and usage events, so every analytics metric has a hand-computable expectation.
//
//	alpha: signup 2024-05-10; usage 2024-05-20 (100c), 2024-06-05 (200c)  [cohort 05, active 05+06]
//	beta : signup 2024-05-25; usage 2024-05-28 (50c)                       [cohort 05, active 05 only]
//	gamma: signup 2024-06-15; usage 2024-07-01 (400c)                      [cohort 06, active 07 only]
func fleetFixture() []custActivity {
	return []custActivity{
		{Org: "alpha", Display: "Alpha", Created: mkTime("2024-05-10"), HasCreated: true,
			Usage: []txnPoint{{T: mkTime("2024-05-20"), Cents: 100}, {T: mkTime("2024-06-05"), Cents: 200}}, SpendCents: 300},
		{Org: "beta", Display: "Beta", Created: mkTime("2024-05-25"), HasCreated: true,
			Usage: []txnPoint{{T: mkTime("2024-05-28"), Cents: 50}}, SpendCents: 50},
		{Org: "gamma", Display: "Gamma", Created: mkTime("2024-06-15"), HasCreated: true,
			Usage: []txnPoint{{T: mkTime("2024-07-01"), Cents: 400}}, SpendCents: 400},
	}
}

// TestComputeRetention_RealCohortTriangle is the headline: the cohort-retention
// heatmap is REAL math over signup cohorts × active months — never a fabricated
// curve. Every cell is hand-verified against the fixture.
func TestComputeRetention_RealCohortTriangle(t *testing.T) {
	now := mkTime("2024-07-15")
	grid := computeRetention(fleetFixture(), now, 12)

	if grid.Interval != "month" {
		t.Fatalf("retention interval = %q, want month", grid.Interval)
	}
	byCohort := map[string]retentionCohort{}
	for _, c := range grid.Cohorts {
		byCohort[c.Cohort] = c
	}

	// Cohort 2024-05 (alpha, beta): period0=100% (both active in 05),
	// period1=50% (only alpha active in 06), period2=0% (neither active in 07).
	c05, ok := byCohort["2024-05"]
	if !ok {
		t.Fatalf("missing cohort 2024-05 in %+v", grid.Cohorts)
	}
	if c05.Size != 2 {
		t.Errorf("cohort 2024-05 size = %d, want 2", c05.Size)
	}
	wantC05 := []float64{100, 50, 0}
	if len(c05.Values) != len(wantC05) {
		t.Fatalf("cohort 2024-05 periods = %d, want %d (%v)", len(c05.Values), len(wantC05), c05.Values)
	}
	for k, want := range wantC05 {
		if math.Abs(c05.Values[k]-want) > 0.01 {
			t.Errorf("retention[2024-05][%d] = %.1f, want %.1f", k, c05.Values[k], want)
		}
	}

	// Cohort 2024-06 (gamma): period0=0% (no usage in 06), period1=100% (active in 07).
	c06 := byCohort["2024-06"]
	wantC06 := []float64{0, 100}
	if len(c06.Values) != len(wantC06) {
		t.Fatalf("cohort 2024-06 periods = %d, want %d (%v)", len(c06.Values), len(wantC06), c06.Values)
	}
	for k, want := range wantC06 {
		if math.Abs(c06.Values[k]-want) > 0.01 {
			t.Errorf("retention[2024-06][%d] = %.1f, want %.1f", k, c06.Values[k], want)
		}
	}
}

// TestComputeChurn_RealLogoChurn proves monthly logo churn + rate are real.
//
//	06 vs 05: base {alpha,beta}=2, churned {beta}=1
//	07 vs 06: base {alpha}=1,      churned {alpha}=1
//	rate = churned(2) / base(3) = 66.67%
func TestComputeChurn_RealLogoChurn(t *testing.T) {
	now := mkTime("2024-07-15")
	series, rate := computeChurn(fleetFixture(), now, 6)

	got := map[string]int64{}
	for _, p := range series {
		got[p.T] = p.Value
	}
	if got["2024-06"] != 1 {
		t.Errorf("churn[2024-06] = %d, want 1 (beta churned)", got["2024-06"])
	}
	if got["2024-07"] != 1 {
		t.Errorf("churn[2024-07] = %d, want 1 (alpha churned)", got["2024-07"])
	}
	if math.Abs(rate-66.666) > 0.1 {
		t.Errorf("churn rate = %.2f, want ~66.67", rate)
	}
}

// TestComputeAnalytics_RealFleet drives the whole pure derivation and asserts the
// growth, active-customer, ARPU, top-customer, LTV, and computed-flag outputs.
func TestComputeAnalytics_RealFleet(t *testing.T) {
	now := mkTime("2024-07-15")
	since, interval, _ := rangeWindow("30d", now) // since 2024-06-15, daily
	d := computeAnalytics(analyticsInput{
		acts: fleetFixture(), mrrCents: 0, now: now, since: since, interval: interval, rangeStr: "30d", ledgerOK: true,
	})

	if d.TotalCustomers != 3 {
		t.Errorf("total customers = %d, want 3", d.TotalCustomers)
	}
	// New in the 30d window [06-15, 07-15]: gamma only (alpha/beta signed up in May).
	if d.NewCustomers != 1 {
		t.Errorf("new customers = %d, want 1 (gamma)", d.NewCustomers)
	}
	// MAU (30d): only gamma had usage in-window (07-01). DAU/WAU: none in last 1/7d.
	if d.MAU != 1 {
		t.Errorf("MAU = %d, want 1", d.MAU)
	}
	if d.DAU != 0 || d.WAU != 0 {
		t.Errorf("DAU/WAU = %d/%d, want 0/0", d.DAU, d.WAU)
	}
	// ARPU = totalSpend(750) / MAU(1) = 750.
	if d.ARPUCents != 750 {
		t.Errorf("ARPU = %d, want 750", d.ARPUCents)
	}
	// Top customer by usage = gamma (400c) first.
	if len(d.TopCustomers) != 3 || d.TopCustomers[0].Hint != "gamma" || d.TopCustomers[0].Value != 400 {
		t.Errorf("top customers wrong: %+v", d.TopCustomers)
	}
	// LTV computed only because churn is observed (>0).
	if d.LTVCents == nil {
		t.Error("LTV must be computed when churn is observed")
	}
	// NRR is honest-null (no MRR history).
	if d.NRRPct != nil {
		t.Error("NRR must be honest-null (needs MRR history)")
	}
	// Computed transparency flags.
	if !d.Computed["growth"] || !d.Computed["retention"] || !d.Computed["active"] || !d.Computed["churn"] {
		t.Errorf("computed flags wrong for a real-ledger fleet: %+v", d.Computed)
	}
	if d.Computed["nrr"] {
		t.Error("nrr computed flag must be false")
	}
	// The usage series is over a continuous daily axis (honest zeros, not gaps).
	if len(d.Usage) == 0 {
		t.Error("usage series must have buckets")
	}
	var usageTotal int64
	for _, p := range d.Usage {
		usageTotal += p.Value
	}
	if usageTotal != 400 { // only gamma's 07-01 400c falls in the 30d window
		t.Errorf("in-window usage total = %d, want 400", usageTotal)
	}
}

// TestComputeAnalytics_HonestEmptyNoLedger proves the no-fabrication contract: with
// real signups but NO usage ledger, growth is still computed but retention/active/
// churn/usage are honest-empty and flagged not-computed — NEVER an invented curve.
func TestComputeAnalytics_HonestEmptyNoLedger(t *testing.T) {
	now := mkTime("2024-07-15")
	// Same signups, but strip all usage (ledger empty / unreachable).
	acts := fleetFixture()
	for i := range acts {
		acts[i].Usage = nil
		acts[i].SpendCents = 0
	}
	since, interval, _ := rangeWindow("30d", now)
	d := computeAnalytics(analyticsInput{acts: acts, now: now, since: since, interval: interval, rangeStr: "30d", ledgerOK: false})

	// Growth is real regardless of the ledger.
	if d.TotalCustomers != 3 || !d.Computed["growth"] {
		t.Errorf("growth must still be computed from signups: total=%d computed=%v", d.TotalCustomers, d.Computed["growth"])
	}
	// Ledger-backed metrics are flagged NOT computed.
	for _, k := range []string{"retention", "active", "churn", "usage", "revenue"} {
		if d.Computed[k] {
			t.Errorf("computed[%q] must be false with no ledger", k)
		}
	}
	// And the actual values are honest zero — no fabricated activity.
	if d.MAU != 0 || d.WAU != 0 || d.DAU != 0 {
		t.Errorf("active must be 0 with no usage: dau=%d wau=%d mau=%d", d.DAU, d.WAU, d.MAU)
	}
	var usageTotal int64
	for _, p := range d.Usage {
		usageTotal += p.Value
	}
	if usageTotal != 0 {
		t.Errorf("usage must be all-zero with no ledger, got %d", usageTotal)
	}
	if d.LTVCents != nil {
		t.Error("LTV must be null with no churn observed")
	}
	// Retention cohorts still exist (from signups) but every cell is 0% (no activity).
	for _, c := range d.Retention.Cohorts {
		for k, v := range c.Values {
			if v != 0 {
				t.Errorf("retention[%s][%d] = %.1f, want 0 (no usage → no fabricated retention)", c.Cohort, k, v)
			}
		}
	}
}

// TestSpendSeries_ContinuousHonestBuckets proves the shared spend series buckets
// real usage onto a continuous axis with honest zeros.
func TestSpendSeries_ContinuousHonestBuckets(t *testing.T) {
	now := mkTime("2024-07-05")
	since := mkTime("2024-07-01")
	acts := []custActivity{
		{Usage: []txnPoint{{T: mkTime("2024-07-01"), Cents: 100}, {T: mkTime("2024-07-03"), Cents: 300}}},
	}
	series := spendSeries(acts, since, now, "day")
	// 5 daily buckets 07-01..07-05.
	if len(series) != 5 {
		t.Fatalf("series buckets = %d, want 5 (%+v)", len(series), series)
	}
	got := map[string]int64{}
	for _, p := range series {
		got[p.T] = p.Value
	}
	if got["2024-07-01"] != 100 || got["2024-07-03"] != 300 {
		t.Errorf("spend buckets wrong: %+v", got)
	}
	// 07-02, 07-04, 07-05 are honest zeros (present, not missing).
	if got["2024-07-02"] != 0 || got["2024-07-04"] != 0 {
		t.Errorf("empty days must be honest 0, got %+v", got)
	}
}

// TestBucketHelpers pins the month arithmetic the retention triangle relies on.
func TestBucketHelpers(t *testing.T) {
	if addMonths("2024-05", 2) != "2024-07" {
		t.Errorf("addMonths(2024-05,2) = %q, want 2024-07", addMonths("2024-05", 2))
	}
	if addMonths("2024-11", 3) != "2025-02" {
		t.Errorf("addMonths(2024-11,3) = %q, want 2025-02", addMonths("2024-11", 3))
	}
	if monthsBetween("2024-05", "2024-07") != 2 {
		t.Errorf("monthsBetween(05,07) = %d, want 2", monthsBetween("2024-05", "2024-07"))
	}
	if monthsBetween("2024-11", "2025-02") != 3 {
		t.Errorf("monthsBetween cross-year = %d, want 3", monthsBetween("2024-11", "2025-02"))
	}
	if normalizeRange("bogus") != "30d" {
		t.Errorf("normalizeRange must default to 30d")
	}
	if normalizeRange("90d") != "90d" {
		t.Errorf("normalizeRange must keep 90d")
	}
}
