package risk

// govern_test.go is the regression suite for the unbounded reads on the
// authorization path.
//
// THE DEFECT: every single decide ran three SELECTs with no LIMIT — the whole
// rule table, the whole entry table, the whole suppression table — and every row
// of all three was then evaluated. A tenant could make its own authorization path
// arbitrarily slow, and there was nothing between "one rule" and "a million".

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestTheGovernedPlanesAreBoundedAtTheWrite pins the caps where they belong. A
// read-time truncation would be a tenant's controls silently switching off; a
// write-time refusal is a tenant being told.
func TestTheGovernedPlanesAreBoundedAtTheWrite(t *testing.T) {
	_, s := wireApp(t)
	db := resOf(t, s, Tenant("hanzo/acme")).db

	// Rules, to the cap and one past it.
	base := rule{Name: "r", Stage: StageSignup, Action: ActionReview, Weight: 0.2, Enabled: true,
		All: []term{{Field: "signal.ip", Op: OpEq, Value: "1.1.1.1"}}}
	var held int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rule`).Scan(&held); err != nil {
		t.Fatalf("count: %v", err)
	}
	for i := held; i < ruleCap; i++ {
		r := base
		r.ID = fmt.Sprintf("bulk-%d", i)
		if err := putRule(db, r); err != nil {
			t.Fatalf("rule %d of %d: %v", i, ruleCap, err)
		}
	}
	over := base
	over.ID = "one-too-many"
	if err := putRule(db, over); err == nil {
		t.Fatalf("a tenant wrote rule %d past a cap of %d — the authorization path has no bound", ruleCap+1, ruleCap)
	} else if statusOf(err) != 409 {
		t.Fatalf("the cap refusal answers %d, want 409", statusOf(err))
	}
	// A tenant AT its cap can still replace a rule it already has: the cap bounds
	// growth, and a tenant that cannot fix a bad rule is worse off than one that
	// cannot add a good one.
	fix := base
	fix.ID = "bulk-10"
	fix.Name = "fixed"
	if err := putRule(db, fix); err != nil {
		t.Fatalf("a tenant at its cap could not replace an existing rule: %v", err)
	}

	// And the read is bounded too, for a row that arrived by any other route.
	rules, err := loadRules(db)
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	if len(rules) > ruleCap {
		t.Fatalf("the authorization path read %d rules against a cap of %d", len(rules), ruleCap)
	}
}

// TestListEntriesAreBoundedAcrossEveryList pins that the cap is over the tenant's
// WHOLE entry plane. A per-list cap would be a bound on nothing — the decision
// path loads every list into one map, and a caller can make more lists.
func TestListEntriesAreBoundedAcrossEveryList(t *testing.T) {
	app, _ := wireApp(t)

	values := make([]string, listCap+1)
	for i := range values {
		values[i] = fmt.Sprintf("10.0.%d.%d", i/256, i%256)
	}
	body, _ := json.Marshal(struct {
		Values []string `json:"values"`
	}{values})
	code, out := req(t, app, http.MethodPost, "/v1/risk/lists/ip-deny/entries", "acme", "u_acme", string(body))
	if code != http.StatusConflict {
		t.Fatalf("adding %d entries answered %d, want 409 — the map the authorization path builds has no bound", len(values), code)
	}
	if !strings.Contains(string(out), fmt.Sprint(listCap)) {
		t.Errorf("the refusal does not name the cap, so a tenant cannot act on it: %s", out)
	}

	// A batch within the cap still works, or the bound is just breakage.
	body, _ = json.Marshal(struct {
		Values []string `json:"values"`
	}{[]string{"203.0.113.9"}})
	code, out = req(t, app, http.MethodPost, "/v1/risk/lists/ip-deny/entries", "acme", "u_acme", string(body))
	if code != http.StatusOK {
		t.Fatalf("a normal add answered %d %s", code, out)
	}
}

// TestGovernanceIsLoadedOncePerChange pins the cache AND its invalidation. A
// cache with no invalidation is a worse defect than the unbounded read it
// replaces, so both halves are asserted here rather than only the fast one.
func TestGovernanceIsLoadedOncePerChange(t *testing.T) {
	app, s := wireApp(t)
	r := resOf(t, s, Tenant("hanzo/acme"))

	rules, _, _, _, err := r.governance()
	if err != nil {
		t.Fatalf("governance: %v", err)
	}
	before := len(rules)
	if before == 0 {
		t.Fatal("a seeded tenant has no rules, so this test cannot see a change")
	}

	// A write BEHIND the cache — no dirty() — must not be seen. That is what
	// proves there is a cache at all rather than a re-read every time.
	if err := putRule(r.db, rule{ID: "behind-the-cache", Name: "unseen", Stage: StageSignup,
		Action: ActionReview, Weight: 0.1, Enabled: true,
		All: []term{{Field: "signal.ip", Op: OpEq, Value: "9.9.9.9"}}}); err != nil {
		t.Fatalf("putRule: %v", err)
	}
	again, _, _, _, _ := r.governance()
	if len(again) != before {
		t.Fatalf("the authorization path re-read the rule table (%d then %d) — three unbounded SELECTs per decision is the defect", before, len(again))
	}

	// A write THROUGH the op is seen immediately: every writer calls dirty.
	code, body := req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"name":"through the op","stage":"signup","action":"review","weight":0.3,"enabled":true,
		  "all":[{"field":"signal.ip","op":"eq","value":"8.8.8.8"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("createRule = %d %s", code, body)
	}
	after, _, _, _, _ := r.governance()
	if len(after) <= before {
		t.Fatalf("a rule created through the op is not visible to the decision path (%d then %d) — the cache is stale", before, len(after))
	}

	// The live/shadow switch rides the same cache and the same invalidation.
	if _, _, _, live, _ := r.governance(); live {
		t.Fatal("a fresh tenant is live; shadow is the default and the default is not configurable")
	}
	code, body = req(t, app, http.MethodPut, "/v1/risk/mode", "acme", "u_acme", `{"mode":"live"}`)
	if code != http.StatusOK {
		t.Fatalf("setMode = %d %s", code, body)
	}
	if _, _, _, live, _ := r.governance(); !live {
		t.Fatal("a tenant that went live is still shadow to the decision path — the mode change did not invalidate the cache")
	}
}

// TestOneTenantsGovernanceCacheIsNotAnothers. The cache lives on the resident, so
// there is no map keyed by more than one tenant and therefore no statement that
// could return the wrong tenant's rules.
func TestOneTenantsGovernanceCacheIsNotAnothers(t *testing.T) {
	app, s := wireApp(t)

	code, body := req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"name":"acme only","stage":"signup","action":"block","weight":0.9,"enabled":true,
		  "all":[{"field":"signal.ip","op":"eq","value":"7.7.7.7"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("createRule = %d %s", code, body)
	}

	mine, _, _, _, _ := resOf(t, s, Tenant("hanzo/acme")).governance()
	theirs, _, _, _, _ := resOf(t, s, Tenant("hanzo/beta")).governance()
	for _, r := range theirs {
		if r.Name == "acme only" {
			t.Fatal("one tenant's cached rule set is readable by another")
		}
	}
	if len(mine) == len(theirs) {
		// Both start from the same seed, so equal lengths after A added one means
		// B got it too.
		t.Fatalf("A holds %d rules and B holds %d after A added one — the caches are the same map", len(mine), len(theirs))
	}
}
