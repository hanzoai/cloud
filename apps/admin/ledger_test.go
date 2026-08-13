package admin

import (
	"strings"
	"testing"
	"time"
)

var ledgerWhen = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

// TestLedgerSQL_ReadsTheOneTable proves every ledger read names the canonical table.
// Three files used to spell it themselves, which is how a twin const in this package
// drifted into pointing at a database that did not exist — silently, because a failed
// query is swallowed by the caller's `err == nil` and the board just renders zeros.
func TestLedgerSQL_ReadsTheOneTable(t *testing.T) {
	s := ledgerScope{Since: ledgerWhen}
	for name, q := range map[string]ledgerQuery{
		"totals":  ledgerTotals(s),
		"series":  ledgerSeries(s, "1 DAY"),
		"byOrg":   ledgerByOrg(s, 10),
		"byModel": ledgerByModel(s, 10),
		"byActor": ledgerByActor(s, 10),
	} {
		if !strings.Contains(q.SQL, "FROM "+ledgerTable) {
			t.Errorf("%s must read %s; got %q", name, ledgerTable, q.SQL)
		}
	}
}

// TestLedgerSQL_BindsEveryValue is the injection pin. The window and the tenant are the
// only values a caller supplies and BOTH are bound: the fleet form carries one `?`, the
// tenant form two, and the org slug never appears in the statement text. Everything
// rendered is a server-side constant — the bucket interval and a row cap.
func TestLedgerSQL_BindsEveryValue(t *testing.T) {
	const hostile = "acme' OR 1=1 --"
	fleet := ledgerScope{Since: ledgerWhen}
	tenant := ledgerScope{Since: ledgerWhen, Org: hostile}

	for name, pair := range map[string][2]ledgerQuery{
		"totals":  {ledgerTotals(fleet), ledgerTotals(tenant)},
		"series":  {ledgerSeries(fleet, "1 DAY"), ledgerSeries(tenant, "1 DAY")},
		"byOrg":   {ledgerByOrg(fleet, 10), ledgerByOrg(tenant, 10)},
		"byModel": {ledgerByModel(fleet, 10), ledgerByModel(tenant, 10)},
		"byActor": {ledgerByActor(fleet, 10), ledgerByActor(tenant, 10)},
	} {
		f, ten := pair[0], pair[1]
		if n := strings.Count(f.SQL, "?"); n != 1 || len(f.Args) != 1 {
			t.Errorf("%s fleet: %d binds / %d args, want 1 and 1; got %q", name, n, len(f.Args), f.SQL)
		}
		if n := strings.Count(ten.SQL, "?"); n != 2 || len(ten.Args) != 2 {
			t.Errorf("%s tenant: %d binds / %d args, want 2 and 2; got %q", name, n, len(ten.Args), ten.SQL)
		}
		if strings.Contains(ten.SQL, hostile) || strings.Contains(ten.SQL, "acme") {
			t.Errorf("%s: the org reached the statement text — it must be BOUND; got %q", name, ten.SQL)
		}
		if ten.Args[1] != hostile {
			t.Errorf("%s: org must be bound verbatim, got %v", name, ten.Args[1])
		}
	}
}

// TestLedgerScope_WindowIsTheBound proves the window arrives as the datastore's own
// DateTime literal, and that naming a tenant narrows with an equality rather than a
// pattern — an org is an exact slug, and LIKE would let one tenant's rows answer for
// another's prefix.
func TestLedgerScope_WindowIsTheBound(t *testing.T) {
	w, args := ledgerScope{Since: ledgerWhen}.where()
	if w != " WHERE timestamp >= ?" || args[0] != "2026-08-13 12:00:00" {
		t.Errorf("fleet scope = %q %v", w, args)
	}
	w, args = ledgerScope{Since: ledgerWhen, Org: "acme"}.where()
	if !strings.Contains(w, "organization = ?") || strings.Contains(w, "LIKE") {
		t.Errorf("a tenant is an exact slug, not a pattern; got %q", w)
	}
	if len(args) != 2 || args[1] != "acme" {
		t.Errorf("tenant scope args = %v", args)
	}
}

// TestLedgerCap_ZeroMeansEveryRow. The tenant directory lists the whole fleet, so "no
// cap" is a real ask and not an oversight — a silent default of ten would have shown
// eighty-one orgs as ten.
func TestLedgerCap_ZeroMeansEveryRow(t *testing.T) {
	if got := ledgerByOrg(ledgerScope{Since: ledgerWhen}, 0); strings.Contains(got.SQL, "LIMIT") {
		t.Errorf("an uncapped read must carry no LIMIT; got %q", got.SQL)
	}
	if got := ledgerByOrg(ledgerScope{Since: ledgerWhen}, 10); !strings.Contains(got.SQL, "LIMIT 10") {
		t.Errorf("a capped read must carry its LIMIT; got %q", got.SQL)
	}
}

// TestLedgerSeries_BucketsByTheConstant proves the only interpolated value is the
// server-side bucket, grouped and ordered by it so the console's curve is in time order.
func TestLedgerSeries_BucketsByTheConstant(t *testing.T) {
	for _, iv := range []string{"1 HOUR", "6 HOUR", "1 DAY"} {
		q := ledgerSeries(ledgerScope{Since: ledgerWhen}, iv)
		if !strings.Contains(q.SQL, "INTERVAL "+iv) || !strings.Contains(q.SQL, "GROUP BY ts ORDER BY ts") {
			t.Errorf("series must bucket by INTERVAL %s; got %q", iv, q.SQL)
		}
	}
}

// TestUsagePointsFrom_DateIsADay pins the shape the console slices to get MM-DD. An
// RFC3339 instant here renders "08-13T00:00:00Z" on the axis.
func TestUsagePointsFrom_DateIsADay(t *testing.T) {
	got := usagePointsFrom([]map[string]any{{
		"ts": ledgerWhen, "cost_cents": uint64(1200), "tokens": uint64(9000), "requests": uint64(3),
	}})
	if len(got) != 1 || got[0].Date != "2026-08-13" {
		t.Fatalf("daily bucket must be a calendar day; got %+v", got)
	}
	if got[0].SpendCents != 1200 || got[0].Tokens != 9000 || got[0].Requests != 3 {
		t.Errorf("point = %+v", got[0])
	}
	// No rows is an empty list, never nil: the console charts [] and would read a
	// missing key as an absent field.
	if empty := usagePointsFrom(nil); empty == nil || len(empty) != 0 {
		t.Errorf("no rows must be an empty series, got %v", empty)
	}
}

// TestUsageByModelFrom_NamesTheModel. The split is by model because that is the
// dimension the ledger HAS; the field it replaces was called `product` and was never
// populated, which read as "this fleet sells nothing".
func TestUsageByModelFrom_NamesTheModel(t *testing.T) {
	got := usageByModelFrom([]map[string]any{{
		"model": "claude-opus-4-8", "cost_cents": uint64(102578), "tokens": uint64(4000),
	}})
	if len(got) != 1 || got[0].Model != "claude-opus-4-8" || got[0].SpendCents != 102578 {
		t.Fatalf("model split = %+v", got)
	}
	if empty := usageByModelFrom(nil); empty == nil || len(empty) != 0 {
		t.Errorf("no rows must be an empty split, got %v", empty)
	}
}
