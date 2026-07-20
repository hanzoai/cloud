package metrics

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/admin/core"
)

// TestFillHeadline proves the run-rate headline coercion (driver ints) + the
// ARR = 12×MRR derivation.
func TestFillHeadline(t *testing.T) {
	var rev SaaSRevenue
	fillHeadline(&rev, map[string]any{
		"mrr": int64(4900), "active_subs": uint64(3), "paying": uint64(2), "trials": uint64(1),
	})
	if rev.MRRCents != 4900 || rev.ARRCents != 4900*12 {
		t.Fatalf("mrr/arr wrong: %+v", rev)
	}
	if rev.ActiveSubscriptions != 3 || rev.PayingCustomers != 2 || rev.Trials != 1 {
		t.Fatalf("counts wrong: %+v", rev)
	}
}

func TestByCategoryAndPlanFromRows(t *testing.T) {
	cats := byCategoryFromRows([]map[string]any{
		{"category": "cloud", "mrr": int64(9800), "subs": uint64(2)},
	})
	if len(cats) != 1 || cats[0].Category != "cloud" || cats[0].MRRCents != 9800 || cats[0].Subscriptions != 2 {
		t.Fatalf("category row wrong: %+v", cats)
	}
	plans := byPlanFromRows([]map[string]any{
		{"plan": "pro", "name": "Pro", "category": "cloud", "active": uint64(2), "trialing": uint64(1), "seats": uint64(5), "mrr": int64(9800)},
	})
	if len(plans) != 1 {
		t.Fatalf("want 1 plan, got %d", len(plans))
	}
	p := plans[0]
	if p.Plan != "pro" || p.Name != "Pro" || p.Category != "cloud" || p.Active != 2 || p.Trialing != 1 || p.Seats != 5 || p.MRRCents != 9800 {
		t.Fatalf("plan row wrong: %+v", p)
	}
}

// TestRecentFromRows proves the movement feed maps event→type and NEGATES churn MRR.
func TestRecentFromRows(t *testing.T) {
	rows := []map[string]any{
		{"at": "2026-07-10T00:00:00Z", "org": "acme", "type": core.EvSubscriptionCreated, "plan": "Pro", "category": "cloud", "mrr_delta": int64(4900)},
		{"at": "2026-07-09T00:00:00Z", "org": "beta", "type": core.EvSubscriptionCanceled, "plan": "Team", "category": "cloud", "mrr_delta": int64(3000)},
	}
	out := recentFromRows(rows)
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	if out[0].Type != "created" || out[0].MRRDeltaCents != 4900 {
		t.Fatalf("created row wrong: %+v", out[0])
	}
	if out[1].Type != "canceled" || out[1].MRRDeltaCents != -3000 {
		t.Fatalf("canceled row must negate mrr: %+v", out[1])
	}
}

func TestSortCustomers(t *testing.T) {
	cs := []SaaSCustomer{
		{Org: "a", MRRCents: 100, UsageCents: 0},
		{Org: "b", MRRCents: 500, UsageCents: 0},
		{Org: "c", MRRCents: 500, UsageCents: 999}, // ties on MRR → usage breaks
	}
	sortCustomers(cs)
	if cs[0].Org != "c" || cs[1].Org != "b" || cs[2].Org != "a" {
		t.Fatalf("order wrong: %s,%s,%s", cs[0].Org, cs[1].Org, cs[2].Org)
	}
}

// TestStateQueriesNoPositionalArgs: the run-rate (state) queries are fully static.
func TestStateQueriesNoPositionalArgs(t *testing.T) {
	for name, sql := range map[string]string{
		"headline": headlineSQL(), "byCategory": byCategorySQL(), "byPlan": byPlanSQL(),
		"orgCount": orgCountSQL(), "perOrgSubs": perOrgSubsSQL(),
	} {
		if !strings.Contains(sql, core.BillingEventsTable) {
			t.Fatalf("%s must read %s", name, core.BillingEventsTable)
		}
		if strings.Contains(sql, "?") {
			t.Fatalf("%s (run-rate) must take no positional args: %q", name, sql)
		}
	}
}

// TestWindowedQueriesOnePositionalArg: the windowed queries bind exactly ONE time
// arg (injection-safe — the since bound is never interpolated).
func TestWindowedQueriesOnePositionalArg(t *testing.T) {
	for name, sql := range map[string]string{
		"movement": movementSQL(), "recent": recentSQL(), "usage": usageSQL(), "perOrgUsage": perOrgUsageSQL(),
	} {
		if n := strings.Count(sql, "?"); n != 1 {
			t.Fatalf("%s must bind exactly ONE positional time arg, got %d: %q", name, n, sql)
		}
		if !strings.Contains(sql, "timestamp >= ?") {
			t.Fatalf("%s time bound must be positional: %q", name, sql)
		}
	}
}

func TestNormalizeAndEmpty(t *testing.T) {
	m := normalize(SaaSMetrics{})
	if m.Revenue.ByCategory == nil || m.Subs.ByPlan == nil || m.Subs.Recent == nil || m.Customers == nil || m.Gaps == nil {
		t.Fatal("normalize must replace nil slices with empty (honest [] not null)")
	}
	e := empty("now", "30d", core.SrcOf("billing-warehouse", errUnconfigured, 0, "now"))
	if e.Currency != "usd" || e.Window != "30d" || len(e.Sources) != 1 || e.Sources[0].OK {
		t.Fatalf("empty snapshot wrong: %+v", e)
	}
}

func TestNormalizeWindow(t *testing.T) {
	for in, want := range map[string]string{"24h": "24h", "7d": "7d", "30d": "30d", "": "30d", "90d": "30d"} {
		if got := normalizeWindow(in); got != want {
			t.Fatalf("normalizeWindow(%q) = %q, want %q", in, got, want)
		}
	}
}
