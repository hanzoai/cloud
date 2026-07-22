package leaderboard

import (
	"strings"
	"testing"
	"time"
)

// hostileOrg is a slug that WOULD break out of the query if it were ever
// string-interpolated instead of bound.
const hostileOrg = "acme'; DROP TABLE hanzo.usage_rollup_daily;--"

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// argsHave reports whether the bound-args slice contains v.
func argsHave(args []any, v string) bool {
	for _, a := range args {
		if s, ok := a.(string); ok && s == v {
			return true
		}
	}
	return false
}

func TestResolveMetric_Allowlist(t *testing.T) {
	ok := map[string]string{"tokens": "total_tokens", "requests": "requests", "cost": "cost_cents", "": "total_tokens", "COST": "cost_cents"}
	for in, want := range ok {
		got, valid := resolveMetric(in)
		if !valid || got != want {
			t.Fatalf("resolveMetric(%q) = %q,%v; want %q,true", in, got, valid, want)
		}
	}
	// Anything outside the allowlist is REJECTED — it can never reach the SQL text.
	for _, bad := range []string{"total_tokens; DROP TABLE x", "cost_cents--", "prompt_tokens", "1;--", "tokens OR 1=1", "model"} {
		if col, valid := resolveMetric(bad); valid {
			t.Fatalf("resolveMetric(%q) accepted as %q; must be rejected", bad, col)
		}
	}
}

// TestBuilders_OrgBoundNeverInterpolated is the injection-safety bar: for EVERY
// org-scoped builder, a hostile org lands in the bound args and NEVER in the SQL
// text, and organization is the LEADING predicate.
func TestBuilders_OrgBoundNeverInterpolated(t *testing.T) {
	w, err := resolvePeriod("month", day(2026, 1, 31))
	if err != nil {
		t.Fatal(err)
	}
	type bcase struct {
		name string
		sql  string
		args []any
	}
	s1, a1 := buildUserBoardSQL(hostileOrg, w, "total_tokens", 10)
	s2, a2 := buildSelfAggSQL(hostileOrg, hostileOrg+"/eve", w)
	s3, a3 := buildAboveCountSQL(hostileOrg, w, "cost_cents", 999)
	s4, a4 := buildUserCountSQL(hostileOrg, w)
	s5, a5 := buildActivitySQL(hostileOrg, hostileOrg+"/eve", w)
	s6, a6 := buildOrgAggSQL(hostileOrg, w)
	cases := []bcase{
		{"userBoard", s1, a1}, {"selfAgg", s2, a2}, {"aboveCount", s3, a3},
		{"userCount", s4, a4}, {"activity", s5, a5}, {"orgAgg", s6, a6},
	}
	for _, c := range cases {
		if strings.Contains(c.sql, "DROP TABLE") || strings.Contains(c.sql, hostileOrg) {
			t.Fatalf("%s: hostile org INTERPOLATED into SQL: %s", c.name, c.sql)
		}
		if !strings.Contains(c.sql, "organization = ?") {
			t.Fatalf("%s: missing bound org predicate: %s", c.name, c.sql)
		}
		if !argsHave(c.args, hostileOrg) {
			t.Fatalf("%s: hostile org not in bound args: %#v", c.name, c.args)
		}
		// organization must be the LEADING WHERE predicate (tenant gate + index key).
		where := c.sql[strings.Index(c.sql, "WHERE ")+len("WHERE "):]
		if !strings.HasPrefix(where, "organization = ?") {
			t.Fatalf("%s: organization is not the leading predicate: %s", c.name, where)
		}
	}
}

// TestOrgBoardSQL_InListBound proves the opted-in org set is bound one-? per org,
// never interpolated — even a hostile org in the visibility set stays in args.
func TestOrgBoardSQL_InListBound(t *testing.T) {
	w, _ := resolvePeriod("all", day(2026, 6, 1))
	sql, args := buildOrgBoardSQL(w, "cost_cents", 5, []string{hostileOrg, "beta"})
	if strings.Contains(sql, hostileOrg) || strings.Contains(sql, "DROP TABLE") {
		t.Fatalf("hostile org interpolated: %s", sql)
	}
	if !strings.Contains(sql, "organization IN (?,?)") {
		t.Fatalf("org IN list not bound: %s", sql)
	}
	if !argsHave(args, hostileOrg) || !argsHave(args, "beta") {
		t.Fatalf("org args missing: %#v", args)
	}
	// "all" period → no lower day bound, upper bound always present.
	if strings.Contains(sql, "day >= ?") {
		t.Fatalf("all period should not have a lower day bound: %s", sql)
	}
	if !strings.Contains(sql, "day < ?") {
		t.Fatalf("upper day bound missing: %s", sql)
	}
	// nil orgs (SuperAdmin) → no IN restriction at all.
	sqlAll, _ := buildOrgBoardSQL(w, "requests", 5, nil)
	if strings.Contains(sqlAll, " IN (") {
		t.Fatalf("nil orgs must not restrict: %s", sqlAll)
	}
}

// TestAboveCount_ThresholdBound proves the self-rank threshold is a bound param.
func TestAboveCount_ThresholdBound(t *testing.T) {
	w, _ := resolvePeriod("week", day(2026, 3, 15))
	sql, args := buildAboveCountSQL("acme", w, "total_tokens", 123456)
	if !strings.Contains(sql, "HAVING sum(total_tokens) > ?") {
		t.Fatalf("threshold not bound: %s", sql)
	}
	if strings.Contains(sql, "123456") {
		t.Fatalf("threshold interpolated: %s", sql)
	}
	found := false
	for _, a := range args {
		if n, ok := a.(int64); ok && n == 123456 {
			found = true
		}
	}
	if !found {
		t.Fatalf("threshold not in args: %#v", args)
	}
	// org-restricted org-above-count binds each org too.
	os2, oa2 := buildOrgAboveCountSQL(w, "cost_cents", 7, []string{hostileOrg})
	if strings.Contains(os2, hostileOrg) {
		t.Fatalf("hostile org interpolated in org-above-count: %s", os2)
	}
	if !argsHave(oa2, hostileOrg) {
		t.Fatalf("org not bound in org-above-count: %#v", oa2)
	}
}

func TestResolvePeriod(t *testing.T) {
	now := day(2026, 6, 15).Add(13 * time.Hour) // mid-day
	cases := []struct {
		in       string
		label    string
		hasFrom  bool
		spanDays int
	}{
		{"", "month", true, 30},
		{"month", "month", true, 30},
		{"week", "week", true, 7},
		{"day", "day", true, 1},
		{"all", "all", false, 0},
	}
	for _, c := range cases {
		w, err := resolvePeriod(c.in, now)
		if err != nil {
			t.Fatalf("resolvePeriod(%q): %v", c.in, err)
		}
		if w.Label != c.label || w.HasFrom != c.hasFrom {
			t.Fatalf("resolvePeriod(%q) = %+v; want label=%s hasFrom=%v", c.in, w, c.label, c.hasFrom)
		}
		// To is always exclusive end = start of tomorrow (today included).
		if !w.To.Equal(day(2026, 6, 16)) {
			t.Fatalf("resolvePeriod(%q).To = %v; want 2026-06-16", c.in, w.To)
		}
		if c.hasFrom {
			gotDays := int(w.To.Sub(w.From).Hours() / 24)
			if gotDays != c.spanDays {
				t.Fatalf("resolvePeriod(%q) span = %d days; want %d", c.in, gotDays, c.spanDays)
			}
		}
	}
	if _, err := resolvePeriod("year", now); err == nil {
		t.Fatal("unknown period must be rejected")
	}
}

func TestResolveRange(t *testing.T) {
	now := day(2026, 6, 15)
	// default span = 90 days back, to = tomorrow.
	w, err := resolveRange("", "", now)
	if err != nil || !w.HasFrom {
		t.Fatalf("default range: %+v %v", w, err)
	}
	if !w.To.Equal(day(2026, 6, 16)) {
		t.Fatalf("default To = %v", w.To)
	}
	// explicit from/to.
	w, err = resolveRange("2026-01-01", "2026-01-31", now)
	if err != nil {
		t.Fatal(err)
	}
	if !w.From.Equal(day(2026, 1, 1)) || !w.To.Equal(day(2026, 2, 1)) {
		t.Fatalf("explicit range = %v..%v", w.From, w.To)
	}
	// span clamp: a 5-year request is clamped to 366 days.
	w, _ = resolveRange("2020-01-01", "2026-01-01", now)
	if w.To.Sub(w.From) > 366*24*time.Hour {
		t.Fatalf("range not clamped: %v..%v", w.From, w.To)
	}
	// invalid.
	if _, err := resolveRange("not-a-date", "", now); err == nil {
		t.Fatal("invalid from must error")
	}
	if _, err := resolveRange("2026-02-01", "2026-01-01", now); err == nil {
		t.Fatal("to before from must error")
	}
}

func TestClampLimit(t *testing.T) {
	cases := map[int]int{0: 10, -5: 10, 5: 5, 100: 100, 101: 100, 9999: 100}
	for in, want := range cases {
		if got := clampLimit(in, 10); got != want {
			t.Fatalf("clampLimit(%d) = %d; want %d", in, got, want)
		}
	}
}
