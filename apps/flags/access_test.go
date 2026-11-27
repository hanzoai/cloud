// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// principalOf is a validated principal of org, as the plane carries one.
func principalOf(org, user string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user})
}

// betaFor is On(beta) for org and user, failing the test on an error.
func betaFor(t *testing.T, org, user string) bool {
	t.Helper()
	on, err := On(principalOf(org, user), Beta)
	if err != nil {
		t.Fatalf("On(beta) for %s/%s: %v", org, user, err)
	}
	return on
}

// target writes the beta switch as the admin cockpit does: rules on org and user
// as anchored, escaped regular expressions, and a rollout.
func target(t *testing.T, orgs, users []string, rollout int) {
	t.Helper()
	alt := func(xs []string) string {
		q := make([]string, len(xs))
		for i, x := range xs {
			q[i] = regexp.QuoteMeta(x)
		}
		return "^(?:" + strings.Join(q, "|") + ")$"
	}
	var groups []map[string]any
	rule := func(key string, xs []string) {
		if len(xs) == 0 {
			return
		}
		groups = append(groups, map[string]any{
			"properties":         []map[string]any{{"key": key, "type": "person", "operator": "regex", "value": alt(xs)}},
			"rollout_percentage": 100,
		})
	}
	rule("org", orgs)
	rule("user", users)
	if rollout > 0 {
		groups = append(groups, map[string]any{"properties": []any{}, "rollout_percentage": rollout})
	}
	def, err := json.Marshal(map[string]any{"active": len(groups) > 0, "filters": map[string]any{"groups": groups}})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPlatformSwitch(Beta, def, "z@hanzo.ai"); err != nil {
		t.Fatalf("set beta: %v", err)
	}
}

// NOTHING STORED IS OFF, for everyone.
func TestBetaIsOffWithNothingStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		for _, org := range []string{"acme", "admin", "hanzo"} {
			if betaFor(t, org, "u1") {
				t.Fatalf("beta is on for %s with nothing stored", org)
			}
		}
	})
}

// A SUPERADMIN TARGETS BETA BY ORG, BY PERSON AND BY ROLLOUT, and the rules match
// exactly: an org whose name differs only in case is another org.
func TestASuperAdminTargetsBeta(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		target(t, []string{"acme", "a.b"}, []string{"u_z"}, 0)
		for _, c := range []struct {
			org, user string
			want      bool
		}{
			{"acme", "u1", true},
			{"a.b", "u1", true},
			{"axb", "u1", false},   // the dot is a dot, not any character
			{"ACME", "u1", false},  // case-distinct orgs are distinct
			{"acme2", "u1", false}, // anchored
			{"globex", "u1", false},
			{"globex", "u_z", true}, // a person named by id, in any org
			{"globex", "u_Z", false},
		} {
			if got := betaFor(t, c.org, c.user); got != c.want {
				t.Errorf("beta for %s/%s = %v, want %v", c.org, c.user, got, c.want)
			}
		}

		target(t, nil, nil, 100)
		if !betaFor(t, "globex", "u1") || !betaFor(t, "hanzo", "u2") {
			t.Error("a 100% rollout leaves a caller out")
		}

		target(t, nil, nil, 0)
		if betaFor(t, "acme", "u1") || betaFor(t, "globex", "u_z") {
			t.Error("an inactive definition is on for someone")
		}
	})
}

// THE ROLLOUT BUCKETS THE CUSTOMER. Every member of a tenant org answers alike;
// in a shared sign-up org each person is bucketed alone, so a rollout never
// sweeps everyone who ever signed up onto one side; and a shared-org caller with
// no person behind it has no subject at all.
func TestTheRolloutBucketsTheOrgOrThePersonInASharedOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		if got := subjectOf("acme", "u1"); got != "acme" {
			t.Errorf("subject of a tenant org's member = %q, want the org", got)
		}
		if got := subjectOf("admin", "u1"); got != "admin" {
			t.Errorf("subject in the admin org = %q, want the org", got)
		}
		if got := subjectOf("hanzo", "u1"); got != "u1" {
			t.Errorf("subject in the shared hanzo org = %q, want the person", got)
		}

		target(t, nil, nil, 50)
		first := betaFor(t, "acme", "u0")
		for i := 1; i < 40; i++ {
			if betaFor(t, "acme", fmt.Sprintf("u%d", i)) != first {
				t.Fatal("two members of one org answered a rollout differently")
			}
		}
		seen := map[bool]int{}
		for i := 0; i < 40; i++ {
			seen[betaFor(t, "hanzo", fmt.Sprintf("u%d", i))]++
		}
		if seen[true] == 0 || seen[false] == 0 {
			t.Fatalf("a 50%% rollout put every person of the shared org on one side: %v", seen)
		}
		if got := subjectOf("hanzo", ""); got != "" {
			t.Errorf("subject in the shared org with no person = %q, want none", got)
		}
	})
}

// NOBODY TURNS BETA ON FOR THEMSELVES. An admin of acme writing `beta` into
// acme's own definitions — the one store it may write — moves nothing, because
// the switch is read from the platform's; and the platform's one write path
// refuses everyone but a SuperAdmin.
func TestNobodyTurnsBetaOnForThemselves(t *testing.T) {
	app, _ := mountTrailed(t)
	every := `{"key":"beta","active":true,"filters":{"groups":[{"rollout_percentage":100}]}}`
	if code := as(t, app, "orgadmin", http.MethodPut, "/v1/flag/defs/beta", "acme", every); code != http.StatusOK {
		t.Fatalf("an org admin writing its own store = %d, want 200", code)
	}
	if betaFor(t, "acme", "u_orgadmin") {
		t.Fatal("SECURITY: an org admin turned beta on for its org by writing its own store")
	}

	for _, who := range []zip.Caller{
		{Org: "acme", User: "u_member"},
		{Org: "acme", User: "u_orgadmin", OrgAdmin: true},
		{Org: "admin", User: "u_member"}, // a member of the admin org who is not a SuperAdmin
	} {
		_, err := set(zip.WithCaller(context.Background(), who), &client.FlagSetIn{Key: Beta, Definition: []byte(every), Actor: who.User})
		if err == nil {
			t.Fatalf("SECURITY: %+v wrote a platform switch", who)
		}
		if betaFor(t, "acme", "u1") {
			t.Fatalf("SECURITY: a refused write by %+v turned beta on", who)
		}
	}
	super := zip.Caller{Org: "admin", User: "u_super", Admin: true}
	if _, err := set(zip.WithCaller(context.Background(), super), &client.FlagSetIn{Key: Beta, Definition: []byte(every), Actor: "z@hanzo.ai"}); err != nil {
		t.Fatalf("a SuperAdmin's write: %v", err)
	}
	if !betaFor(t, "acme", "u1") {
		t.Fatal("a SuperAdmin's write did not take")
	}
}

// THE CALLER IS THE VALIDATED PRINCIPAL. An org with no validated user behind it
// is the shape a forged X-Org-Id arrives in, and it is refused, not evaluated.
func TestOnNeedsAValidatedPrincipal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		target(t, []string{"acme"}, nil, 0)
		if _, err := On(zip.WithCaller(context.Background(), zip.Caller{Org: "acme"}), Beta); err == nil {
			t.Fatal("SECURITY: an org with no validated user behind it was evaluated")
		}
		if _, err := On(context.Background(), Beta); err == nil {
			t.Fatal("a call with no caller was evaluated")
		}
	})
}

// A STORE THAT CANNOT BE READ IS AN ERROR, never an answer — and the route says
// so with 503, which every reader takes as off.
func TestAnUnreadableStoreIsAnError(t *testing.T) {
	app := mountHTTP(t)
	prev := mounted
	mounted = &Client{}
	t.Cleanup(func() { mounted = prev })
	if _, err := On(principalOf("acme", "u1"), Beta); err == nil {
		t.Fatal("an engine that cannot read its store answered")
	}
	if code, body := do(t, app, http.MethodGet, "/v1/flag/platform", "acme", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/flag/platform with no store = %d %s, want 503", code, body)
	}
}

// GET /v1/flag/platform ANSWERS THE CALLER, AND ONLY A SIGNED-IN ONE.
func TestThePlatformRouteAnswersTheCaller(t *testing.T) {
	app := mountHTTP(t)
	target(t, []string{"acme"}, nil, 0)
	read := func(org string) map[string]bool {
		t.Helper()
		code, body := do(t, app, http.MethodGet, "/v1/flag/platform", org, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/flag/platform as %s = %d %s", org, code, body)
		}
		var out platformOut
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		return out.Flags
	}
	if got := read("acme"); !got[Beta] {
		t.Errorf("acme reads %v, want beta on", got)
	}
	if got := read("globex"); got[Beta] {
		t.Errorf("globex reads %v, want beta off", got)
	}
	if got := read("globex"); len(got) != 1+len(Betas) {
		t.Errorf("the route answers %v, want exactly the principal switches", got)
	}

	for name, hdr := range map[string]map[string]string{
		"no credential":       {},
		"a forged org header": {"X-Org-Id": "acme"},
		"a project key":       {"x-api-key": "pk-acme"},
	} {
		rq := httptest.NewRequest(http.MethodGet, "/v1/flag/platform", nil)
		for k, v := range hdr {
			rq.Header.Set(k, v)
		}
		resp, err := app.Test(rq, httpCfg)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: GET /v1/flag/platform = %d, want 401", name, resp.StatusCode)
		}
	}
}

// A PRINCIPAL SWITCH HAS NO DEPLOYMENT-WIDE VALUE. Read as a deployment switch it
// is off whatever is stored, the value op refuses it, and the board shows what is
// stored and that it is decided per caller.
func TestAPrincipalSwitchHasNoDeploymentValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		target(t, nil, nil, 100) // on for everyone
		if Bool(Beta) {
			t.Fatal("beta read as a deployment switch")
		}
		if _, err := value(principalOf("acme", "u1"), &client.FlagValueIn{Key: Beta, Kind: "bool", Default: "true"}); err == nil {
			t.Fatal("the value op answered for a principal switch")
		}
		var sw SwitchView
		for _, s := range Board().Switches {
			if s.Key == Beta {
				sw = s
			}
		}
		if !sw.Principal || sw.Value != "true" || sw.Source != "flags" || len(sw.Definition) == 0 {
			t.Fatalf("the board shows beta as %+v", sw)
		}
		apps := map[string]bool{}
		for _, b := range Betas {
			apps[BetaKey(b.ID)] = true
		}
		for _, s := range Board().Switches {
			if s.Key != Beta && !apps[s.Key] && (s.Principal || len(s.Definition) != 0) {
				t.Fatalf("%s is shown as a principal switch", s.Key)
			}
		}
	})
}

// ASKING THE WRONG QUESTION IS AN ERROR, not an off.
func TestOnRefusesAKeyThatIsNotAPrincipalSwitch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		for _, key := range []string{"waitlist_open", "nosuchswitch", ""} {
			if _, err := On(principalOf("acme", "u1"), key); err == nil {
				t.Errorf("On(%q) answered", key)
			}
		}
	})
}

// A PRINCIPAL SWITCH THAT COULD BE ON WITH NOTHING STORED does not register.
func TestRegisterRefusesAPrincipalSwitchThatCouldBeOn(t *testing.T) {
	for name, d := range map[string]Def{
		"default on": {Key: "t_on", Type: TypeBool, Default: "true", Principal: true},
		"an env":     {Key: "t_env", Type: TypeBool, Default: "false", Env: "T_ENV", Principal: true},
		"not a bool": {Key: "t_int", Type: TypeInt, Default: "false", Principal: true},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: registered", name)
				}
			}()
			Register(d)
		}()
	}
	if d, ok := lookupDef(Beta); !ok || !d.Principal {
		t.Fatal("beta is not registered as a principal switch")
	}
}

// ONLY THE COCKPIT'S SHAPE IS STORED. Every definition the engine would read as
// wider than the rules it names — on for everyone, case-folded, a variant, an
// implicit rollout, a shared org — is refused before it lands, and nothing moves.
func TestOnlyTheCockpitsShapeIsStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		rule := func(key, op, value string) string {
			return `{"active":true,"filters":{"groups":[{"properties":[{"key":"` + key + `","type":"person","operator":"` + op + `","value":` + value + `}],"rollout_percentage":100}]}}`
		}
		for name, def := range map[string]string{
			"nothing, which the engine reads as on": `{}`,
			"a key alone":                           `{"key":"beta"}`,
			"no active and no groups":               `{"filters":{"groups":[]}}`,
			"no active beside a rule":               `{"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme)$"}],"rollout_percentage":100}]}}`,
			"active with no filters":                `{"active":true}`,
			"active with no groups":                 `{"active":true,"filters":{"groups":[]}}`,
			"an implicit rollout":                   `{"active":true,"filters":{"groups":[{"properties":[]}]}}`,
			"a rollout past 100":                    `{"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":150}]}}`,
			"exact, which folds case":               rule("org", "exact", `["acme"]`),
			"icontains":                             rule("org", "icontains", `"acme"`),
			"an email rule":                         rule("email", "regex", `"^(?:z@hanzo\\.ai)$"`),
			"an unanchored pattern":                 rule("org", "regex", `"acme"`),
			"an anchored non-literal":               rule("org", "regex", `"^(?:ac.e)$"`),
			"an escape out of a group":              rule("org", "regex", `"^(?:acme)|(x)$"`),
			"an empty name":                         rule("org", "regex", `"^(?:acme|)$"`),
			"a shared org":                          rule("org", "regex", `"^(?:acme|hanzo)$"`),
			"two rules in a group":                  `{"active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme)$"},{"key":"user","type":"person","operator":"regex","value":"^(?:u1)$"}],"rollout_percentage":100}]}}`,
			"a variant":                             `{"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}],"multivariate":{"variants":[{"key":"control","rollout_percentage":100}]}}}`,
			"a payload":                             `{"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}],"payloads":{"true":1}}}`,
			"group aggregation":                     `{"active":true,"aggregation_group_type_index":0,"filters":{"groups":[{"properties":[],"rollout_percentage":100}]}}`,
			"a cohort":                              rule("id", "in", `[1]`),
			"not an object":                         `[1]`,
		} {
			if err := SetPlatformSwitch(Beta, json.RawMessage(def), "z@hanzo.ai"); err == nil {
				t.Errorf("%s: stored %s", name, def)
			}
		}
		if betaFor(t, "acme", "u1") || betaFor(t, "x", "u1") {
			t.Fatal("a refused definition moved beta")
		}
		for name, def := range map[string]string{
			"off":                `{"active":false,"filters":{"groups":[]}}`,
			"everyone, said so":  `{"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}]}}`,
			"orgs, people, some": `{"key":"beta","active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme|a\\.b)$"}],"rollout_percentage":100},{"properties":[{"key":"user","type":"person","operator":"regex","value":"^(?:u_z)$"}],"rollout_percentage":100},{"properties":[],"rollout_percentage":5}]}}`,
		} {
			if err := SetPlatformSwitch(Beta, json.RawMessage(def), "z@hanzo.ai"); err != nil {
				t.Errorf("%s: refused: %v", name, err)
			}
		}
	})
}

// A SHARED ORG IS NO RULE'S TARGET. A rule naming one — however it got into the
// store — matches nobody, because a member of a shared org is asked about as the
// person they are and never as the org.
func TestARuleNamingASharedOrgMatchesNobody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		st, err := c.platform()
		if err != nil {
			t.Fatal(err)
		}
		legacy := `{"active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:hanzo|acme)$"}],"rollout_percentage":100}]}}`
		if err := st.Upsert(Beta, json.RawMessage(legacy), "t"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			if betaFor(t, "hanzo", fmt.Sprintf("stranger%d", i)) {
				t.Fatal("SECURITY: a rule naming the shared sign-up org let a stranger in")
			}
		}
		if !betaFor(t, "acme", "u1") {
			t.Fatal("the tenant org beside it lost its beta")
		}
	})
}

// A MALFORMED NEIGHBOUR DOES NOT TAKE BETA DOWN: the switch is evaluated alone.
func TestAMalformedNeighbourLeavesBetaAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		target(t, []string{"acme"}, nil, 0)
		st, err := c.platform()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Upsert("waitlist_open", json.RawMessage(`{"active":"yes"}`), "t"); err != nil {
			t.Fatal(err)
		}
		if !betaFor(t, "acme", "u1") {
			t.Fatal("a malformed waitlist_open turned acme's beta off")
		}
	})
}

// THE ORG NAMED LIKE THE DEPLOYMENT IS NO TENANT. The platform switches live at
// OrgNamespace(platform, platform); an org called "platform" acting in a project
// called "platform" would otherwise write them as its own.
func TestTheReservedOrgIsNoTenant(t *testing.T) {
	app := mountHTTP(t)
	every := []byte(`{"key":"beta","active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}]}}`)
	rq := httptest.NewRequest(http.MethodPut, "/v1/flag/defs/beta", strings.NewReader(string(every)))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", principal.Reserved)
	rq.Header.Set("X-User-Id", "u_platform")
	rq.Header.Set("X-User-IsOrgAdmin", "true")
	rq.Header.Set("X-Project-Id", principal.Reserved)
	resp, err := app.Test(rq, httpCfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the org %q wrote a flag: %d, want 403", principal.Reserved, resp.StatusCode)
	}
	if betaFor(t, "acme", "u1") {
		t.Fatal("SECURITY: a tenant named like the deployment turned beta on for everyone")
	}
}

// A BETA WRITE IS ON THE TRAIL FIRST, with what it replaced; a trail that does not
// take it refuses the write.
func TestABetaWriteIsAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		rec, err := audit.Open(t.TempDir(), "audit-flags", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		c.trail = rec
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "u_super", Email: "z@hanzo.ai", Admin: true})
		first := `{"active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme)$"}],"rollout_percentage":100}]}}`
		second := `{"active":false,"filters":{"groups":[]}}`
		for _, def := range []string{first, second} {
			if _, err := set(super, &client.FlagSetIn{Key: Beta, Definition: []byte(def), Actor: "z@hanzo.ai"}); err != nil {
				t.Fatalf("set: %v", err)
			}
		}
		rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "flag.platform.put"})
		if err != nil || len(rows) != 2 {
			t.Fatalf("trail holds %d beta writes (%v), want 2", len(rows), err)
		}
		var last audit.Record
		for _, r := range rows {
			if len(r.Before) > 0 {
				last = r
			}
		}
		if last.Actor.Email != "z@hanzo.ai" || !strings.Contains(string(last.Before), "acme") || !strings.Contains(string(last.After), `"active":false`) {
			t.Fatalf("the overwrite's record does not say what it replaced: %+v", last)
		}

		c.trail = audit.Remote("admin", gone{})
		if _, err := set(super, &client.FlagSetIn{Key: Beta, Definition: []byte(first), Actor: "z@hanzo.ai"}); err == nil {
			t.Fatal("a beta write the trail did not take was made")
		}
		if betaFor(t, "acme", "u1") {
			t.Fatal("an unaudited write turned beta on")
		}
	})
}

// A KEY REPEATED IN ANOTHER CASE READS AS WHAT WAS CHECKED. encoding/json folds
// key case and keeps the last; the store kept both and the engine read the other.
// What is stored is the checked document re-encoded, so whatever the bytes sent,
// the engine evaluates what was checked — off here, for everyone.
func TestACaseVariantKeyCannotTurnBetaOn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		for _, def := range []string{
			`{"active":true,"Active":false,"filters":{"groups":[]}}`,
			`{"Active":true,"active":false,"filters":{"groups":[]}}`,
			`{"active":false,"filters":{"groups":[]},"Filters":{"groups":[{"properties":[],"rollout_percentage":100}]}}`,
			`{"active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme)$","Operator":"exact"}],"rollout_percentage":100}]}}`,
		} {
			_ = SetPlatformSwitch(Beta, json.RawMessage(def), "z@hanzo.ai")
			if betaFor(t, "globex", "u1") {
				t.Fatalf("SECURITY: %s turned beta on for globex", def)
			}
		}
		st, err := mounted.platform()
		if err != nil {
			t.Fatal(err)
		}
		row, found, err := st.Get(Beta)
		if err != nil || !found {
			t.Fatalf("nothing stored: %v", err)
		}
		var stored map[string]any
		if err := json.Unmarshal(row.Definition, &stored); err != nil {
			t.Fatal(err)
		}
		for k := range stored {
			if k != "active" && k != "filters" && k != "key" {
				t.Fatalf("the stored definition carries %q: %s", k, row.Definition)
			}
		}
	})
}

// NO COMPOSER REACHES THE PLATFORM STORE AS A TENANT. An experiment writes its
// assignment flag through PutDef with the tenant's org and project; for an org
// named like the deployment acting in a project of the same name, that would be
// the platform's own store.
func TestNoComposerReachesThePlatformStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		every := json.RawMessage(`{"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}],"multivariate":{"variants":[{"key":"control","rollout_percentage":100}]}}}`)
		if err := PutDef(principal.Reserved, principal.Reserved, Beta, every, "x"); err == nil {
			t.Fatal("SECURITY: PutDef wrote the platform store")
		}
		if _, _, err := GetDef(principal.Reserved, principal.Reserved, Beta); err == nil {
			t.Fatal("GetDef read the platform store")
		}
		if _, err := Assign(principal.Reserved, principal.Reserved, Beta, "x", nil); err == nil {
			t.Fatal("Assign read the platform store")
		}
		if betaFor(t, "acme", "u1") {
			t.Fatal("SECURITY: beta is on after a composer's write")
		}
	})
}

// AN ORG PARKED WITH NOBODY BEHIND IT IS NO PRINCIPAL. An anonymous read naming an
// org parks that org on the context (cloud.As); co-resident, the switch would read
// the slot and answer for the org. It refuses, as it does across a socket.
func TestAnOrgWithNobodyBehindItIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		target(t, []string{"acme"}, nil, 0)
		ctx := principal.WithActing(context.Background(), "acme")
		if on, err := On(ctx, Beta); err == nil || on {
			t.Fatalf("an org parked with no user behind it was evaluated: on=%v err=%v", on, err)
		}
	})
}

// THE KEY IS READ ONCE. A padded key is the switch it names, checked, audited and
// stored as that switch — never an orphan row beside it.
func TestAPaddedKeyIsTheSwitchItNames(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "u_super", Admin: true})
		if _, err := set(super, &client.FlagSetIn{Key: " beta ", Definition: []byte(`{"active":true}`), Actor: "z@hanzo.ai"}); err == nil {
			t.Fatal("a padded key skipped the targeting check")
		}
		def := `{"active":true,"filters":{"groups":[{"properties":[{"key":"org","type":"person","operator":"regex","value":"^(?:acme)$"}],"rollout_percentage":100}]}}`
		if _, err := set(super, &client.FlagSetIn{Key: " beta ", Definition: []byte(def), Actor: "z@hanzo.ai"}); err != nil {
			t.Fatalf("set: %v", err)
		}
		if !betaFor(t, "acme", "u1") {
			t.Fatal("a padded key was stored somewhere beta is not read from")
		}
	})
}
