package leaderboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestLeaderboard_FailClosedNoPrincipal: an org header without a validated user id
// is the anonymous-forge path — it must be refused (401), not scoped to the forged org.
func TestLeaderboard_FailClosedNoPrincipal(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/leaderboard", map[string]string{"X-Org-Id": "acme"}) // NO X-User-Id
	if code != http.StatusUnauthorized {
		t.Fatalf("forge path must be 401, got %d", code)
	}
}

// TestLeaderboard_HonestEmptyWhenDatastoreDown: a warehouse outage is honest-empty
// (available:false, no rows) — never a fabricated leaderboard.
func TestLeaderboard_HonestEmptyWhenDatastoreDown(t *testing.T) {
	installFakeDS(t, nil)
	datastoreEnabled = func() bool { return false } // restored by installFakeDS cleanup
	app := mountApp(t)
	code, body := doGet(t, app, "/v1/usage/leaderboard?scope=personal", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var v LeaderboardView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Available || len(v.Rows) != 0 {
		t.Fatalf("must be honest-empty: %+v", v)
	}
}

// TestLeaderboard_OrgAlwaysBoundNeverInterpolated is the handler-level isolation bar:
// every datastore query the handler issues for a caller binds the caller's org as an
// arg and never places it (or an injection) in the SQL text.
func TestLeaderboard_OrgAlwaysBoundNeverInterpolated(t *testing.T) {
	f := installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY user_id ORDER BY") {
			return []map[string]any{userRow("acme/alice", 3, 300, 50), userRow("acme/bob", 1, 100, 10)}
		}
		return nil
	})
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/leaderboard?scope=personal&metric=tokens&period=month", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	sawBound := false
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "acme") {
			t.Fatalf("org interpolated into SQL: %s", c.sql)
		}
		if argsHave(c.args, "acme") {
			sawBound = true
		}
	}
	if !sawBound {
		t.Fatal("no query bound the caller's org")
	}
}

// TestLeaderboard_HostileOrgHeaderBound: even a SQL-injection org (from the trusted
// header path) is bound, never interpolated — the handler-level twin of the builder
// test.
func TestLeaderboard_HostileOrgHeaderBound(t *testing.T) {
	f := installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/leaderboard?scope=personal", principalHeaders(hostileOrg, "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	bound := false
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "DROP TABLE") {
			t.Fatalf("HOSTILE ORG INTERPOLATED: %s", c.sql)
		}
		if argsHave(c.args, hostileOrg) {
			bound = true
		}
	}
	if !bound {
		t.Fatal("hostile org was never bound (did any query run?)")
	}
}

// TestLeaderboard_NoCrossTenantBleed: two callers in different orgs; neither's queries
// ever reference the other's org.
func TestLeaderboard_NoCrossTenantBleed(t *testing.T) {
	f := installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY user_id ORDER BY") {
			return []map[string]any{userRow("x/y", 1, 1, 1)}
		}
		return nil
	})
	app := mountApp(t)
	doGet(t, app, "/v1/usage/leaderboard?scope=personal", principalHeaders("acme", "a"))
	nAcme := len(f.allCalls())
	doGet(t, app, "/v1/usage/leaderboard?scope=personal", principalHeaders("victim", "v"))
	for i, c := range f.allCalls() {
		who, other := "acme", "victim"
		if i >= nAcme {
			who, other = "victim", "acme"
		}
		if argsHave(c.args, other) || strings.Contains(c.sql, other) {
			t.Fatalf("call %d (%s request) leaked %q: sql=%s args=%v", i, who, other, c.sql, c.args)
		}
	}
}

// TestLeaderboard_NamingPolicy end-to-end: caller sees SELF named, an opted-in peer
// by their handle, and a non-opted peer as Anonymous.
func TestLeaderboard_NamingPolicy(t *testing.T) {
	installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY user_id ORDER BY") {
			return []map[string]any{userRow("acme/alice", 5, 500, 50), userRow("acme/bob", 3, 300, 30), userRow("acme/carol", 1, 100, 10)}
		}
		return nil
	})
	app := mountApp(t)
	seedUserOptin(t, "acme/bob", "acme", "BobBuilder", true) // opted-in with a handle

	code, body := doGet(t, app, "/v1/usage/leaderboard?scope=personal&metric=tokens", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var v LeaderboardView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	var alice, bob, carol *LeaderboardRow
	for i := range v.Rows {
		switch v.Rows[i].Handle {
		case "alice":
			alice = &v.Rows[i]
		case "BobBuilder":
			bob = &v.Rows[i]
		case "Anonymous":
			carol = &v.Rows[i]
		}
	}
	if alice == nil || !alice.Self {
		t.Fatalf("caller must appear as self ('alice'): %+v", v.Rows)
	}
	if bob == nil || bob.Anonymous {
		t.Fatalf("opted-in peer must show handle 'BobBuilder': %+v", v.Rows)
	}
	if carol == nil || !carol.Anonymous {
		t.Fatalf("non-opted peer must be Anonymous: %+v", v.Rows)
	}
	// A non-admin, non-cost board never leaks a peer's spend.
	if bob.CostCents != 0 || carol.CostCents != 0 {
		t.Fatalf("peer cost leaked on a non-cost board: bob=%+v carol=%+v", bob, carol)
	}
}

// TestLeaderboard_GlobalCostRequiresAdmin: the cross-org SPEND board is SuperAdmin-only.
func TestLeaderboard_GlobalCostRequiresAdmin(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/leaderboard?scope=global&metric=cost", principalHeaders("acme", "alice"))
	if code != http.StatusForbidden {
		t.Fatalf("non-admin global cost must be 403, got %d", code)
	}
	code2, _ := doGet(t, app, "/v1/usage/leaderboard?scope=global&metric=cost", withHeader(principalHeaders("acme", "alice"), "X-User-IsAdmin", "true"))
	if code2 != http.StatusOK {
		t.Fatalf("superadmin global cost must be 200, got %d", code2)
	}
}

// TestLeaderboard_GlobalNonSuperOnlyOptedInOrgs: a non-super caller's global board is
// EMPTY until an org opts in; the org-ranking query never runs over all orgs.
func TestLeaderboard_GlobalNonSuperOnlyOptedInOrgs(t *testing.T) {
	f := installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY organization ORDER BY") {
			return []map[string]any{{"organization": "acme", "requests": uint64(9), "total_tokens": uint64(900), "cost_cents": uint64(90)}}
		}
		return nil
	})
	app := mountApp(t)

	code, body := doGet(t, app, "/v1/usage/leaderboard?scope=global&metric=tokens", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var v LeaderboardView
	_ = json.Unmarshal(body, &v)
	if len(v.Rows) != 0 {
		t.Fatalf("no opted-in orgs → empty board, got %+v", v.Rows)
	}
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "GROUP BY organization ORDER BY") {
			t.Fatal("org-ranking query ran with NO opted-in orgs (would expose every org)")
		}
	}

	// Opt acme in → it appears, by its chosen display.
	seedOrgOptin(t, "acme", "Acme Inc", true)
	_, body2 := doGet(t, app, "/v1/usage/leaderboard?scope=global&metric=tokens", principalHeaders("acme", "alice"))
	var v2 LeaderboardView
	_ = json.Unmarshal(body2, &v2)
	if len(v2.Rows) != 1 || v2.Rows[0].Handle != "Acme Inc" {
		t.Fatalf("opted-in org must appear as its display: %+v", v2.Rows)
	}
}

// TestLeaderboard_BadMetricRejected: an out-of-allowlist metric is a 400, never a query.
func TestLeaderboard_BadMetricRejected(t *testing.T) {
	f := installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/leaderboard?metric=total_tokens;DROP", principalHeaders("acme", "alice"))
	if code != http.StatusBadRequest {
		t.Fatalf("bad metric must be 400, got %d", code)
	}
	if len(f.allCalls()) != 0 {
		t.Fatalf("no query should run for a rejected metric, ran %d", len(f.allCalls()))
	}
}
