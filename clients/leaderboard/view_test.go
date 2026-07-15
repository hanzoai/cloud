package leaderboard

import (
	"testing"
	"time"
)

func TestDisplayUser_PrivacyPolicy(t *testing.T) {
	handles := map[string]string{"acme/bob": "BobBuilder"}
	base := nameCtx{selfUserID: "acme/alice", handles: handles}

	// self, no chosen handle → username; self=true, not anonymous.
	if h, anon, self := displayUser("acme/alice", base); h != "alice" || anon || !self {
		t.Fatalf("self default: got %q anon=%v self=%v", h, anon, self)
	}
	// self WITH chosen handle → the handle.
	withHandle := base
	withHandle.selfHandle = "AliceZ"
	if h, _, self := displayUser("acme/alice", withHandle); h != "AliceZ" || !self {
		t.Fatalf("self handle: got %q self=%v", h, self)
	}
	// opted-in peer → their chosen handle, not anonymous, not self.
	if h, anon, self := displayUser("acme/bob", base); h != "BobBuilder" || anon || self {
		t.Fatalf("opted peer: got %q anon=%v self=%v", h, anon, self)
	}
	// NON-opted peer → Anonymous (identity withheld).
	if h, anon, self := displayUser("acme/carol", base); h != "Anonymous" || !anon || self {
		t.Fatalf("non-opted peer must be Anonymous: got %q anon=%v self=%v", h, anon, self)
	}
	// admin viewer (named) → the member's username even without opt-in.
	admin := nameCtx{selfUserID: "acme/alice", named: true, handles: map[string]string{}}
	if h, anon, _ := displayUser("acme/carol", admin); h != "carol" || anon {
		t.Fatalf("admin view: got %q anon=%v", h, anon)
	}
}

func TestDisplayOrg(t *testing.T) {
	displays := map[string]string{"acme": "Acme Inc"}
	if got := displayOrg("acme", true, displays); got != "acme" {
		t.Fatalf("super sees slug: %q", got)
	}
	if got := displayOrg("acme", false, displays); got != "Acme Inc" {
		t.Fatalf("non-super sees display: %q", got)
	}
	if got := displayOrg("beta", false, displays); got != "beta" {
		t.Fatalf("no display falls back to slug: %q", got)
	}
}

func TestNameOf(t *testing.T) {
	cases := map[string]string{"acme/alice": "alice", "alice": "alice", "a/b/c": "c", "x/": "x/"}
	for in, want := range cases {
		if got := nameOf(in); got != want {
			t.Fatalf("nameOf(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestBuildUserRows_CostVisibility(t *testing.T) {
	aggs := []aggRow{
		{id: "acme/alice", requests: 10, tokens: 1000, costCents: 500},
		{id: "acme/bob", requests: 5, tokens: 400, costCents: 200},
	}
	// caller = alice, not admin, tokens board (costVisible=false).
	nc := nameCtx{selfUserID: "acme/alice", handles: map[string]string{}}
	rows := buildUserRows(aggs, "tokens", nc, false)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows")
	}
	// ranks are 1,2 in input order; metric = tokens.
	if rows[0].Rank != 1 || rows[0].Metric != 1000 || rows[1].Rank != 2 || rows[1].Metric != 400 {
		t.Fatalf("ranks/metric wrong: %+v", rows)
	}
	// self (alice) sees own cost; bob (anonymized peer) does NOT expose cost.
	if !rows[0].Self || rows[0].CostCents != 500 {
		t.Fatalf("self cost hidden: %+v", rows[0])
	}
	if rows[1].CostCents != 0 || !rows[1].Anonymous {
		t.Fatalf("peer cost must be withheld + anonymous on a non-cost board: %+v", rows[1])
	}
	// A cost board (costVisible=true) exposes the ranked cost on every row.
	rowsCost := buildUserRows(aggs, "cost", nc, true)
	if rowsCost[1].CostCents != 200 || rowsCost[1].Metric != 200 {
		t.Fatalf("cost board should carry cost as the metric: %+v", rowsCost[1])
	}
}

func TestBuildOrgRows(t *testing.T) {
	aggs := []aggRow{{id: "acme", requests: 9, tokens: 900, costCents: 90}, {id: "beta", requests: 3, tokens: 100, costCents: 10}}
	displays := map[string]string{"acme": "Acme Inc", "beta": "Beta LLC"}
	// non-super: displays used, cost withheld.
	rows := buildOrgRows(aggs, "tokens", false, displays, false)
	if rows[0].Handle != "Acme Inc" || rows[0].CostCents != 0 {
		t.Fatalf("non-super org row: %+v", rows[0])
	}
	// super: slugs + cost visible.
	rowsSuper := buildOrgRows(aggs, "cost", true, displays, true)
	if rowsSuper[0].Handle != "acme" || rowsSuper[0].CostCents != 90 || rowsSuper[0].Metric != 90 {
		t.Fatalf("super org row: %+v", rowsSuper[0])
	}
}

func TestBuildActivitySeries_GapFill(t *testing.T) {
	// window covers 4 days: 01,02,03,04 (To exclusive = 05).
	w := window{From: day(2026, 1, 1), To: day(2026, 1, 5), HasFrom: true}
	rows := []map[string]any{
		{"day": day(2026, 1, 2), "requests": uint64(5), "total_tokens": uint64(50), "cost_cents": uint64(20)},
		{"day": day(2026, 1, 4), "requests": uint64(3), "total_tokens": uint64(90), "cost_cents": uint64(10)},
	}
	days, tot := buildActivitySeries(w, rows)
	if len(days) != 4 {
		t.Fatalf("want 4 gap-filled days, got %d", len(days))
	}
	want := []struct {
		day string
		tok int64
	}{{"2026-01-01", 0}, {"2026-01-02", 50}, {"2026-01-03", 0}, {"2026-01-04", 90}}
	for i, wnt := range want {
		if days[i].Day != wnt.day || days[i].Tokens != wnt.tok {
			t.Fatalf("day[%d] = %+v; want %s tok=%d", i, days[i], wnt.day, wnt.tok)
		}
	}
	if tot.ActiveDays != 2 || tot.Tokens != 140 || tot.Requests != 8 || tot.CostCents != 30 {
		t.Fatalf("totals wrong: %+v", tot)
	}
	if tot.MaxTokens != 90 || tot.MaxRequests != 5 {
		t.Fatalf("heatmap ceilings wrong: %+v", tot)
	}
}

func TestDayKey(t *testing.T) {
	if got := dayKey(day(2026, 7, 14)); got != "2026-07-14" {
		t.Fatalf("time: %q", got)
	}
	if got := dayKey("2026-07-14 00:00:00"); got != "2026-07-14" {
		t.Fatalf("string: %q", got)
	}
	if got := dayKey(time.Time{}); got != "0001-01-01" {
		t.Fatalf("zero time: %q", got)
	}
	if got := dayKey(42); got != "" {
		t.Fatalf("unknown type must be empty: %q", got)
	}
}
