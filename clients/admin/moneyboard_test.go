package admin

import (
	"testing"

	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/customer"
	"github.com/hanzoai/cloud/clients/admin/finance"
	"github.com/hanzoai/cloud/clients/admin/money"
	"github.com/hanzoai/cloud/clients/admin/revenue"
)

// TestFoldGrants_SplitsBucketsAndTallies proves the two money buckets are kept apart
// (a trial comp is not cash), that a legacy row with no source is treated as trial the
// same way the grants ledger treats it, and that the per-org tally keys on the TARGET
// org rather than the staff actor.
func TestFoldGrants_SplitsBucketsAndTallies(t *testing.T) {
	totals, byOrg := foldGrants([]customer.GrantRow{
		{Org: "acme", AmountCents: 5000, Source: "prepaid"},
		{Org: "acme", AmountCents: 2500, Source: "trial"},
		{Org: "globex", AmountCents: 1000, Source: ""}, // legacy row — a comp, so trial
	})

	if totals.total != 8500 {
		t.Errorf("granted total = %d, want 8500", totals.total)
	}
	if totals.prepaid != 5000 {
		t.Errorf("prepaid = %d, want 5000 (real money only)", totals.prepaid)
	}
	if totals.trial != 3500 {
		t.Errorf("trial = %d, want 3500 (2500 + the sourceless legacy 1000)", totals.trial)
	}
	if totals.count != 3 {
		t.Errorf("grant count = %d, want 3", totals.count)
	}
	if byOrg["acme"].cents != 7500 || byOrg["acme"].count != 2 {
		t.Errorf("acme tally = %+v, want 7500 over 2 grants", byOrg["acme"])
	}
	if byOrg["globex"].cents != 1000 {
		t.Errorf("globex tally = %+v, want 1000", byOrg["globex"])
	}
}

// TestFoldGrants_Empty proves an empty ledger folds to real zeros, not a panic.
func TestFoldGrants_Empty(t *testing.T) {
	totals, byOrg := foldGrants(nil)
	if totals.total != 0 || totals.count != 0 || len(byOrg) != 0 {
		t.Errorf("empty fold = %+v / %v, want zeros", totals, byOrg)
	}
}

// TestOrgRows_JoinsGrantsAndSortsBySpend proves the per-org join and the biggest-spender
// ordering, and that an org with no grants reads as a real zero rather than dropping out.
func TestOrgRows_JoinsGrantsAndSortsBySpend(t *testing.T) {
	rows := orgRows(
		[]revenue.RevenueCustomer{
			{Org: "small", Display: "Small", SpendCents: 100, BalanceCents: 50},
			{Org: "big", Display: "Big", SpendCents: 9000, BalanceCents: 10, MRRCents: 2000},
		},
		map[string]grantTally{"big": {cents: 7500, count: 2}},
	)

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Org != "big" {
		t.Errorf("rows[0] = %q, want the biggest spender first", rows[0].Org)
	}
	if rows[0].GrantedCents != 7500 || rows[0].Grants != 2 {
		t.Errorf("big grants = %d over %d, want 7500 over 2", rows[0].GrantedCents, rows[0].Grants)
	}
	if rows[1].GrantedCents != 0 {
		t.Errorf("an org with no grants must read 0, got %d", rows[1].GrantedCents)
	}
}

// TestFoldMoney proves the consolidation itself: derived values are derived, and every
// number the board did NOT derive is carried through from the domain that owns it, so
// /v1/admin/money can never disagree with /v1/admin/revenue or /v1/admin/finance.
func TestFoldMoney(t *testing.T) {
	runway := 42.5
	rev := revenue.RevenueData{
		TotalBalancesCents: 30000,
		TotalSpendCents:    12000,
		MRRCents:           2500,
		Customers:          9,
		PayingCustomers:    4,
		ARPUCents:          3000,
		PerCustomer:        []revenue.RevenueCustomer{{Org: "acme", SpendCents: 12000}},
	}
	fin := finance.FinanceData{
		Cost: finance.FinanceCost{
			Period:     "2026-07",
			TotalCents: 8000,
			DigitalOcean: finance.DoCost{
				MonthToDateSpendCents: 4400,
				CreditRemainingCents:  260000,
				AvgDailyBurnCents:     160,
			},
		},
		Derived: finance.FinanceDerived{
			GrossMarginCents: 4000, GrossMarginPct: 33.3, Profitable: true, RunwayDays: &runway,
		},
	}
	grants := []customer.GrantRow{{Org: "acme", AmountCents: 6000, Source: "prepaid"}}

	got := foldMoney(rev, fin, grants, money.Cents(9999), "2026-07-27T00:00:00Z")

	// Revenue: ARR is the ONE derivation here.
	if got.Revenue.ARRCents != 30000 {
		t.Errorf("ARR = %d, want 30000 (12 x 2500 MRR)", got.Revenue.ARRCents)
	}
	if got.Revenue.RealizedCents != 12000 || got.Revenue.Paying != 4 {
		t.Errorf("revenue = %+v, want realized 12000 / paying 4", got.Revenue)
	}

	// Credits: consumed IS realized spend — stated once, from one source.
	if got.Credits.ConsumedCents != got.Revenue.RealizedCents {
		t.Errorf("consumed %d must equal realized %d — same number, one source",
			got.Credits.ConsumedCents, got.Revenue.RealizedCents)
	}
	if got.Credits.OutstandingCents != 30000 {
		t.Errorf("outstanding = %d, want the 30000 balance customers still hold", got.Credits.OutstandingCents)
	}
	if got.Credits.GrantedCents != 6000 || got.Credits.GrantedPrepaidCents != 6000 || got.Credits.GrantedTrialCents != 0 {
		t.Errorf("credits = %+v, want 6000 granted, all prepaid", got.Credits)
	}

	// Infrastructure cost + the reserve.
	if got.Infra.VendorCogsCents != 8000 || got.Infra.DOMonthToDateCents != 4400 {
		t.Errorf("infra = %+v, want 8000 COGS / 4400 DO month-to-date", got.Infra)
	}
	if got.Infra.TreasuryReserveCents != 9999 {
		t.Errorf("reserve = %d, want 9999", got.Infra.TreasuryReserveCents)
	}
	if got.Infra.Period != "2026-07" {
		t.Errorf("period = %q, want the finance period carried through", got.Infra.Period)
	}

	// Margin is carried through, never recomputed — the two boards cannot disagree.
	if got.Margin.GrossCents != 4000 || got.Margin.GrossPct != 33.3 || !got.Margin.Profitable {
		t.Errorf("margin = %+v, want finance's own 4000 / 33.3%% / profitable", got.Margin)
	}
	if got.Margin.RunwayDays == nil || *got.Margin.RunwayDays != 42.5 {
		t.Errorf("runway must carry through from finance; got %v", got.Margin.RunwayDays)
	}

	if len(got.ByOrg) != 1 || got.ByOrg[0].GrantedCents != 6000 {
		t.Errorf("byOrg = %+v, want acme joined to its 6000 grant", got.ByOrg)
	}
	if got.GeneratedAt != "2026-07-27T00:00:00Z" {
		t.Errorf("generatedAt = %q", got.GeneratedAt)
	}
}

// TestMergeSources_NamespacesByDomain proves the collision this board would otherwise
// have: revenue and finance BOTH read an upstream called "commerce", and an operator
// must be able to tell which of the two degraded.
func TestMergeSources_NamespacesByDomain(t *testing.T) {
	var out []core.SourceStatus
	out = mergeSources(out, "revenue", []core.SourceStatus{{Name: "commerce", OK: true, Rows: 9}})
	out = mergeSources(out, "finance", []core.SourceStatus{{Name: "commerce", OK: false, Error: "boom"}})

	if len(out) != 2 {
		t.Fatalf("sources = %d, want 2", len(out))
	}
	if out[0].Name != "revenue:commerce" || out[1].Name != "finance:commerce" {
		t.Fatalf("names = %q / %q, want domain-qualified", out[0].Name, out[1].Name)
	}
	if !out[0].OK || out[1].OK {
		t.Error("each domain's own status must survive the merge independently")
	}
}
