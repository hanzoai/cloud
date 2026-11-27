// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// promote makes key a public beta, as the cockpit writes it: the opt-in group,
// beside a grant to each of users.
func promote(t *testing.T, key string, users ...string) {
	t.Helper()
	groups := []map[string]any{{
		"properties":         []map[string]any{{"key": "optin", "type": "person", "operator": "regex", "value": optinRule}},
		"rollout_percentage": 100,
	}}
	if len(users) > 0 {
		groups = append(groups, map[string]any{
			"properties":         []map[string]any{{"key": "user", "type": "person", "operator": "regex", "value": "^(?:" + strings.Join(users, "|") + ")$"}},
			"rollout_percentage": 100,
		})
	}
	def, err := json.Marshal(map[string]any{"active": true, "filters": map[string]any{"groups": groups}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set(superCtx, &client.FlagSetIn{Key: key, Definition: def, Actor: "z@hanzo.ai"}); err != nil {
		t.Fatalf("promote %s: %v", key, err)
	}
}

// call drives one request as user acting in org; role "orgadmin" administers it.
func call(t *testing.T, app *zip.App, method, path, org, user, role, body string) (int, map[string]any) {
	t.Helper()
	rq := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", user)
	}
	if role == "orgadmin" {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(rq, httpCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// STAFF HAS EVERY BETA, whatever is stored: a SuperAdmin is the admin org's own.
// Acting in the admin org without the SuperAdmin bit is no one's staff.
func TestStaffHasEveryBeta(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		for _, d := range Defs() {
			if !d.Principal {
				continue
			}
			if on, err := On(superCtx, d.Key); err != nil || !on {
				t.Fatalf("staff does not have %s: %v %v", d.Key, on, err)
			}
		}
		if onFor(t, BetaKey("crm"), "admin", "u_member") {
			t.Fatal("acting in the admin org without the SuperAdmin bit opened a beta")
		}
	})
}

// AN OPT-IN OPENS EVERY PUBLIC BETA AND NO PRIVATE ONE, for the person who chose
// it and nobody beside them, and the answer names which betas are public.
func TestAnOptinOpensPublicBetasAndNoPrivateOne(t *testing.T) {
	app, _ := mountTrailed(t)
	promote(t, BetaKey("crm"))
	grantFor(t, BetaKey("erp"), "u_granted", true)

	code, got := call(t, app, http.MethodPut, "/v1/flag/optin", "acme", "u1", "", `{"on":true}`)
	if code != http.StatusOK || got["on"] != true || got["blocked"] != false {
		t.Fatalf("opting in answered %d %v", code, got)
	}
	if pub, _ := got["public"].([]any); len(pub) != 1 || pub[0] != "crm" {
		t.Fatalf("public betas = %v, want [crm]", got["public"])
	}
	cases := []struct {
		user, key string
		want      bool
	}{
		{"u1", BetaKey("crm"), true},        // public, opted in
		{"u1", BetaKey("erp"), false},       // private: opting in grants nothing
		{"u2", BetaKey("crm"), false},       // public, not opted in
		{"u_granted", BetaKey("erp"), true}, // a grant stands as it did
	}
	for _, c := range cases {
		if got := onFor(t, c.key, "acme", c.user); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.user, c.key, got, c.want)
		}
	}
	if code, got := call(t, app, http.MethodPut, "/v1/flag/optin", "acme", "u1", "", `{"on":false}`); code != http.StatusOK || got["on"] != false {
		t.Fatalf("opting out answered %d %v", code, got)
	}
	if onFor(t, BetaKey("crm"), "acme", "u1") {
		t.Fatal("a public beta stayed on after opting out")
	}
}

// A BLOCKED ORG GETS NO PUBLIC BETA BY OPTING IN. The block is on the trail; a
// member may neither opt in under it nor lift it; the person's own choice is kept
// and applies again once it is lifted.
func TestABlockedOrgGetsNoPublicBetaByOptingIn(t *testing.T) {
	app, rec := mountTrailed(t)
	promote(t, BetaKey("crm"))
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin", "acme", "u1", "", `{"on":true}`); code != http.StatusOK {
		t.Fatalf("opting in answered %d", code)
	}
	if code, got := call(t, app, http.MethodPut, "/v1/flag/optin/org", "acme", "u_admin", "orgadmin", `{"blocked":true}`); code != http.StatusOK || got["blocked"] != true {
		t.Fatalf("an org admin blocking answered %d %v", code, got)
	}
	if onFor(t, BetaKey("crm"), "acme", "u1") {
		t.Fatal("a public beta is on in a blocked org")
	}
	if code, got := call(t, app, http.MethodGet, "/v1/flag/optin", "acme", "u1", "", ""); code != http.StatusOK || got["on"] != true || got["blocked"] != true {
		t.Fatalf("the blocked opt-in reads %d %v, want on and blocked", code, got)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin", "acme", "u3", "", `{"on":true}`); code != http.StatusForbidden {
		t.Fatalf("opting in under a block answered %d, want 403", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin/org", "acme", "u3", "", `{"blocked":false}`); code != http.StatusForbidden {
		t.Fatalf("a member lifting the block answered %d, want 403", code)
	}
	rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "flag.optin.org"})
	if err != nil || len(rows) != 1 || rows[0].Resource.ID != "acme" || !strings.Contains(string(rows[0].After), `"blocked":true`) {
		t.Fatalf("the block is not on the trail: %v %+v", err, rows)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin/org", "acme", "u_admin", "orgadmin", `{"blocked":false}`); code != http.StatusOK {
		t.Fatalf("lifting the block answered %d", code)
	}
	if !onFor(t, BetaKey("crm"), "acme", "u1") {
		t.Fatal("the person's opt-in did not apply again once the block was lifted")
	}
}

// ONE PERSON CANNOT WRITE ANOTHER'S OPT-IN: the row is the caller's subject,
// whatever the body names.
func TestOnePersonCannotWriteAnothersOptin(t *testing.T) {
	app, _ := mountTrailed(t)
	promote(t, BetaKey("crm"))
	call(t, app, http.MethodPut, "/v1/flag/optin", "acme", "u2", "", `{"on":true,"user":"u1","subject":"u1"}`)
	if _, got := call(t, app, http.MethodGet, "/v1/flag/optin", "acme", "u1", "", ""); got["on"] != false {
		t.Fatalf("u2's write opted u1 in: %v", got)
	}
	if onFor(t, BetaKey("crm"), "acme", "u1") {
		t.Fatal("u1 has a public beta they never opted in to")
	}
}

// AN ORG ADMIN CANNOT GRANT. Its one power here withholds; lifting a block opens
// nothing for a member who did not opt in, and promoting a beta is refused.
func TestAnOrgAdminCannotGrant(t *testing.T) {
	app, _ := mountTrailed(t)
	promote(t, BetaKey("crm"))
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin/org", "acme", "u_admin", "orgadmin", `{"blocked":false}`); code != http.StatusOK {
		t.Fatalf("an org admin unblocking answered %d", code)
	}
	if onFor(t, BetaKey("crm"), "acme", "u4") || onFor(t, BetaKey("crm"), "acme", "u_admin") {
		t.Fatal("an org admin's write opened a public beta for someone who did not opt in")
	}
	admin := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u_admin", Email: "a@acme.com"})
	def := []byte(`{"active":true,"filters":{"groups":[{"properties":[{"key":"optin","type":"person","operator":"regex","value":"^(?:true)$"}],"rollout_percentage":100}]}}`)
	_, err := set(admin, &client.FlagSetIn{Key: BetaKey("erp"), Definition: def, Actor: "a@acme.com"})
	if he, ok := errors.AsType[*zip.HTTPError](err); !ok || he.Status != http.StatusForbidden {
		t.Fatalf("an org admin promoting a beta = %v, want 403", err)
	}
}

// A SHARED SIGN-UP ORG CANNOT BE BLOCKED: no admin speaks for strangers. Its
// members opt in as the people they are.
func TestASharedOrgCannotBeBlocked(t *testing.T) {
	app, _ := mountTrailed(t)
	promote(t, BetaKey("crm"))
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin/org", "hanzo", "u_admin", "orgadmin", `{"blocked":true}`); code != http.StatusBadRequest {
		t.Fatalf("blocking the shared org answered %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/flag/optin", "hanzo", "u5", "", `{"on":true}`); code != http.StatusOK {
		t.Fatalf("opting in from the shared org answered %d", code)
	}
	if !onFor(t, BetaKey("crm"), "hanzo", "u5") {
		t.Fatal("a shared-org member who opted in has no public beta")
	}
}

// PROMOTION IS A SUPERADMIN'S, ON THE TRAIL: the opt-in group rides the one
// definition write path, and a write that is not a SuperAdmin's is refused.
func TestPromotionIsASuperAdminsAndAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		rec, err := audit.Open(t.TempDir(), "audit-flags", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		c.trail = rec
		member := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u1"})
		def := []byte(`{"active":true,"filters":{"groups":[{"properties":[{"key":"optin","type":"person","operator":"regex","value":"^(?:true)$"}],"rollout_percentage":100}]}}`)
		if _, err := set(member, &client.FlagSetIn{Key: BetaKey("crm"), Definition: def}); err == nil {
			t.Fatal("a member promoted a beta")
		}
		if rows, _, _ := rec.Query(context.Background(), audit.Filter{Action: "flag.platform.put"}); len(rows) != 0 {
			t.Fatalf("a refused promotion landed on the trail as made: %+v", rows)
		}
		promote(t, BetaKey("crm"))
		rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "flag.platform.put"})
		if err != nil || len(rows) != 1 || rows[0].Resource.ID != BetaKey("crm") || !strings.Contains(string(rows[0].After), `"optin"`) {
			t.Fatalf("the promotion is not on the trail: %v %+v", err, rows)
		}
		st, err := c.platform()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := publicBetas(st); err != nil || !slices.Equal(got, []string{"crm"}) {
			t.Fatalf("public betas = %v (%v), want [crm]", got, err)
		}
	})
}

// THE OPT-IN RULE HOLDS ONE VALUE: "optin" is this package's own fact, true or
// absent, and a rule reading it any other way is refused before it lands.
func TestTheOptinRuleHoldsOneValue(t *testing.T) {
	rule := func(v string) json.RawMessage {
		return json.RawMessage(`{"active":true,"filters":{"groups":[{"properties":[{"key":"optin","type":"person","operator":"regex","value":"` + v + `"}],"rollout_percentage":100}]}}`)
	}
	if _, err := targetable(rule(`^(?:true)$`)); err != nil {
		t.Fatalf("the opt-in rule was refused: %v", err)
	}
	for _, v := range []string{`^(?:yes)$`, `^(?:true|false)$`, `true`, `^(?:false)$`} {
		if _, err := targetable(rule(v)); err == nil {
			t.Errorf("an opt-in rule matching %s was stored", v)
		}
	}
}

// THE OPT-IN IS A PERSON'S: an unauthenticated read is 401.
func TestTheOptinNeedsAPerson(t *testing.T) {
	app, _ := mountTrailed(t)
	if code, _ := call(t, app, http.MethodGet, "/v1/flag/optin", "", "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated read answered %d, want 401", code)
	}
}
