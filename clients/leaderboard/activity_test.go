package leaderboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ── pure authorization policy ─────────────────────────────────────────────────

func TestResolveUserSubject(t *testing.T) {
	const self = "acme/alice"
	// self forms are always allowed for a non-admin.
	for _, id := range []string{"", "me", "ME", self, "alice"} {
		if got, status, _ := resolveUserSubject(self, false, "acme", id); status != 0 || got != self {
			t.Fatalf("self id %q: got %q status %d; want self, 0", id, got, status)
		}
	}
	// a non-admin requesting ANOTHER user → 403.
	if _, status, _ := resolveUserSubject(self, false, "acme", "bob"); status != http.StatusForbidden {
		t.Fatalf("non-admin cross-user must be 403, got %d", status)
	}
	// an admin may view another user IN THEIR org (bare name → owner/name).
	if got, status, _ := resolveUserSubject(self, true, "acme", "bob"); status != 0 || got != "acme/bob" {
		t.Fatalf("admin cross-user: got %q status %d", got, status)
	}
	// even an admin cannot reach a user of ANOTHER org.
	if _, status, _ := resolveUserSubject(self, true, "acme", "other/eve"); status != http.StatusForbidden {
		t.Fatalf("cross-org user must be 403, got %d", status)
	}
	// admin requesting a properly-qualified same-org id.
	if got, status, _ := resolveUserSubject(self, true, "acme", "acme/bob"); status != 0 || got != "acme/bob" {
		t.Fatalf("admin qualified same-org: got %q status %d", got, status)
	}
	// self unresolvable (no username) → 400.
	if _, status, _ := resolveUserSubject("", false, "acme", ""); status != http.StatusBadRequest {
		t.Fatalf("missing self identity must be 400, got %d", status)
	}
}

func TestResolveOrgSubject(t *testing.T) {
	if got, status, _ := resolveOrgSubject(false, "acme", ""); status != 0 || got != "acme" {
		t.Fatalf("own org (empty): got %q status %d", got, status)
	}
	if got, status, _ := resolveOrgSubject(false, "acme", "acme"); status != 0 || got != "acme" {
		t.Fatalf("own org (matching): got %q status %d", got, status)
	}
	if _, status, _ := resolveOrgSubject(false, "acme", "victim"); status != http.StatusForbidden {
		t.Fatalf("non-super cross-org must be 403, got %d", status)
	}
	if got, status, _ := resolveOrgSubject(true, "acme", "victim"); status != 0 || got != "victim" {
		t.Fatalf("super cross-org: got %q status %d", got, status)
	}
}

// ── handler-level enforcement ─────────────────────────────────────────────────

// TestActivity_CrossUserForbidden: a non-admin cannot read another user's activity.
func TestActivity_CrossUserForbidden(t *testing.T) {
	f := installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/activity?subject=user&id=bob", principalHeaders("acme", "alice"))
	if code != http.StatusForbidden {
		t.Fatalf("cross-user must be 403, got %d", code)
	}
	if len(f.allCalls()) != 0 {
		t.Fatalf("no datastore query should run for a denied request, ran %d", len(f.allCalls()))
	}
}

// TestActivity_AdminCrossUserBindsOrgAndUser: an org admin may read a member's
// activity, and the query binds BOTH the org and the target user id (org-scoped).
func TestActivity_AdminCrossUserBindsOrgAndUser(t *testing.T) {
	f := installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY day") {
			return []map[string]any{{"day": day(2026, 6, 1), "requests": uint64(2), "total_tokens": uint64(20), "cost_cents": uint64(5)}}
		}
		return nil
	})
	app := mountApp(t)
	h := withHeader(principalHeaders("acme", "alice"), "X-User-IsOrgAdmin", "true")
	code, body := doGet(t, app, "/v1/usage/activity?subject=user&id=bob&from=2026-05-01&to=2026-06-30", h)
	if code != http.StatusOK {
		t.Fatalf("admin cross-user must be 200, got %d body=%s", code, body)
	}
	var v ActivityView
	_ = json.Unmarshal(body, &v)
	if v.ID != "acme/bob" {
		t.Fatalf("resolved id should be acme/bob, got %q", v.ID)
	}
	found := false
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "GROUP BY day") {
			if !argsHave(c.args, "acme") || !argsHave(c.args, "acme/bob") {
				t.Fatalf("activity query must bind org + target user: %v", c.args)
			}
			if strings.Contains(c.sql, "acme") {
				t.Fatalf("org/user interpolated: %s", c.sql)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no activity query ran")
	}
}

// TestActivity_CrossOrgForbidden: a non-super caller cannot read another org's series.
func TestActivity_CrossOrgForbidden(t *testing.T) {
	f := installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doGet(t, app, "/v1/usage/activity?subject=org&id=victim", principalHeaders("acme", "alice"))
	if code != http.StatusForbidden {
		t.Fatalf("cross-org must be 403, got %d", code)
	}
	if len(f.allCalls()) != 0 {
		t.Fatal("no query should run for a denied cross-org request")
	}
}

// TestActivity_ProjectHonestEmpty: subject=project is authorized but HONEST-empty
// (no project attribution in the ledger), never a fabricated series, and runs no query.
func TestActivity_ProjectHonestEmpty(t *testing.T) {
	f := installFakeDS(t, nil)
	app := mountApp(t)
	code, body := doGet(t, app, "/v1/usage/activity?subject=project&id=proj-1", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	var v ActivityView
	_ = json.Unmarshal(body, &v)
	if v.Available || v.Note == "" || len(v.Days) != 0 {
		t.Fatalf("project must be honest-empty with a note: %+v", v)
	}
	if len(f.allCalls()) != 0 {
		t.Fatal("project honest-empty must not query the datastore")
	}
}

// TestActivity_SelfSeriesGapFilled: a caller's own activity is a gap-filled per-day
// series scoped to their org + user id.
func TestActivity_SelfSeriesGapFilled(t *testing.T) {
	f := installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "GROUP BY day") {
			return []map[string]any{{"day": day(2026, 6, 2), "requests": uint64(4), "total_tokens": uint64(40), "cost_cents": uint64(8)}}
		}
		return nil
	})
	app := mountApp(t)
	code, body := doGet(t, app, "/v1/usage/activity?subject=user&from=2026-06-01&to=2026-06-03", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var v ActivityView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if !v.Available || v.ID != "acme/alice" {
		t.Fatalf("self series: %+v", v)
	}
	if len(v.Days) != 3 || v.Totals.ActiveDays != 1 || v.Totals.Tokens != 40 {
		t.Fatalf("gap-fill/totals wrong: days=%d totals=%+v", len(v.Days), v.Totals)
	}
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "GROUP BY day") && !argsHave(c.args, "acme/alice") {
			t.Fatalf("self series not bound to caller: %v", c.args)
		}
	}
}
