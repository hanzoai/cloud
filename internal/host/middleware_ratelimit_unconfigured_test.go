package host

// AN ORG NOBODY CONFIGURED IS STILL HELD.
//
// Once the edge stopped counting validated callers by address, "no rule" would
// have meant "no ceiling" for every org nobody configured — one address's backstop
// per process was all that held it. An unconfigured org now carries a default
// derived from the per-IP knob, counted once for the deployment, and a validated
// caller with no org meets the same default by who it is.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// The default cap is the platform's org_rpm, 10 a minute here.
func TestAnUnconfiguredOrgCarriesTheDefault(t *testing.T) {
	gp := staticPol(t, edge.Policy{OrgRPM: 10})
	if got := gp.Cap("acme"); got != 10 {
		t.Fatalf("Cap(acme) = %d, want 10", got)
	}
	pod := coordtest.Memory(t)
	a, b := limiterProcess(t, pod("cloud-0"), gp), limiterProcess(t, pod("cloud-1"), gp)
	for i := range 10 {
		if code := []process{a, b}[i%2].send(t, "acme", "", "").StatusCode; code != http.StatusOK {
			t.Fatalf("request %d of the default 10: %d", i+1, code)
		}
	}
	resp := a.send(t, "acme", "", "")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("the 11th across two processes: %d limit %q, want 429 naming 10", resp.StatusCode, resp.Header.Get("X-RateLimit-Limit"))
	}
	if code := b.send(t, "globex", "", "").StatusCode; code != http.StatusOK {
		t.Fatalf("another org shares acme's default: %d", code)
	}

	// An org's own org_rpm may lower its ceiling and never raise it past the cap:
	// the free signup that administers its own org could otherwise remove the
	// default by writing a bigger number.
	for _, tc := range []struct {
		name         string
		orgRPM, want int
	}{{"a lower org_rpm", 5, 5}, {"a higher org_rpm", 12, 10}} {
		p := limiterProcess(t, coordtest.Memory(t)("cloud-0"), orgPolicy(t, edge.Policy{OrgRPM: 10}, "initech", edge.Policy{OrgRPM: tc.orgRPM}))
		for i := range tc.want {
			if code := p.send(t, "initech", "", "").StatusCode; code != http.StatusOK {
				t.Fatalf("%s: request %d of %d: %d", tc.name, i+1, tc.want, code)
			}
		}
		if code := p.send(t, "initech", "", "").StatusCode; code != http.StatusTooManyRequests {
			t.Fatalf("%s: request %d: %d, want 429", tc.name, tc.want+1, code)
		}
	}

}

// A validated caller that acts in no org meets the default by who it is; a request
// the boundary resolved to nobody is the edge's to count, not this one's.
func TestAValidatedCallerWithNoOrgMeetsTheDefault(t *testing.T) {
	gp := staticPol(t, edge.Policy{OrgRPM: 10})
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Subject: c.Header("X-Test-Subject")})
		return c.Continue()
	}))
	app.Use(ScopeRateLimit(nil, gp))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	send := func(subject string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
		r.Header.Set("X-Test-Subject", subject)
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for i := range 10 {
		if code := send("4f1c0de0-orgless"); code != http.StatusOK {
			t.Fatalf("org-less request %d of 10: %d", i+1, code)
		}
	}
	if code := send("4f1c0de0-orgless"); code != http.StatusTooManyRequests {
		t.Fatalf("an org-less validated caller past the default: %d, want 429", code)
	}
	if code := send("another-orgless"); code != http.StatusOK {
		t.Fatalf("another org-less caller shares the first's count: %d", code)
	}
	for range 20 {
		if code := send(""); code != http.StatusOK {
			t.Fatalf("nobody was counted by the org ceiling: %d", code)
		}
	}
}

// orgPolicy is a policy store with platform as its boot policy and row written for
// org, as the gateway config route would.
func orgPolicy(t *testing.T, platform edge.Policy, org string, row edge.Policy) *edge.Store {
	t.Helper()
	st, err := edge.New(t.TempDir(), "admin", platform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Put(t.Context(), org, row); err != nil {
		t.Fatal(err)
	}
	return st
}

// ONLY THE PLATFORM RAISES A CAP. A SuperAdmin's org_cap on an org's row is that
// org's cap; its own org_rpm then lowers inside it and is clipped to it, and no
// commerce rule — set by the org's admin through its spend alerts — loosens it.
func TestOnlyThePlatformRaisesAnOrgsCap(t *testing.T) {
	platform := edge.Policy{OrgRPM: 10} // default cap: 10 a minute
	for _, tc := range []struct {
		name string
		row  edge.Policy
		want int
	}{
		{"a cap of 15", edge.Policy{OrgCap: 15}, 15},
		{"a cap of 15, org_rpm 12", edge.Policy{OrgCap: 15, OrgRPM: 12}, 12},
		{"a cap of 15, org_rpm 40", edge.Policy{OrgCap: 15, OrgRPM: 40}, 15},
	} {
		p := limiterProcess(t, coordtest.Memory(t)("cloud-0"), orgPolicy(t, platform, "acme", tc.row))
		for i := range tc.want {
			if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusOK {
				t.Fatalf("%s: request %d of %d: %d", tc.name, i+1, tc.want, code)
			}
		}
		if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusTooManyRequests {
			t.Fatalf("%s: request %d: %d, want 429", tc.name, tc.want+1, code)
		}
	}
}

// A COMMERCE RULE ONLY TIGHTENS. Spend-alert rules are written by the org's own
// admin, so an org-wide rule above the cap, and service rules that would add up past
// it, are held to the cap: the org's whole ceiling is charged on every request.
func TestACommerceRuleOnlyTightensTheCap(t *testing.T) {
	servePlaneRules(t, map[string][]client.ScopeRule{
		"hanzo": {{RateLimitRpm: 1000}, {Service: "agent", RateLimitRpm: 900}},
	}, nil)
	fc := &capCommerce{}
	m := mustClient(t, fc.server(t).URL)
	app := zip.New(zip.Config{})
	app.Use(ScopeRateLimit(m, staticPol(t, edge.Policy{OrgRPM: 10})))
	app.Raw(http.MethodPost, "/v1/agent/run", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	for i := range 10 {
		if code := rateReq(t, app, "hanzo", "").StatusCode; code != http.StatusOK {
			t.Fatalf("request %d of the cap's 10: %d", i+1, code)
		}
	}
	resp := rateReq(t, app, "hanzo", "")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("past the cap under rules of 1000 and 900: %d limit %q, want 429 naming 10", resp.StatusCode, resp.Header.Get("X-RateLimit-Limit"))
	}
}

// fleetApp is ScopeRateLimit over gp, the boundary standing in as headers: the org
// (X-Org-Id), the kind (X-Test-Kind) and the user (X-User-Id).
func fleetApp(t *testing.T, gp *edge.Store) func(org, kind, user string) int {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: c.Header("X-User-Id"),
			Subject: strings.TrimPrefix(c.Header("X-User-Id"), "app:"), Kind: principal.Kind(c.Header("X-Test-Kind"))})
		return c.Continue()
	}))
	app.Use(ScopeRateLimit(nil, gp))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	return func(org, kind, user string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", user)
		r.Header.Set("X-Test-Kind", kind)
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
}

// FIRST-PARTY TRAFFIC CARRIES NO DEFAULT. The reserved admin org — SuperAdmins and
// the platform's machines — and a brand org's applications are not held by the
// default cap; a fleet application is held in a window of its own, never its org's,
// so the bot and the org's people do not spend one budget. A brand org's people
// carry the default like anyone's, and a tenant's application spends its org's
// ceiling with its people, so minting applications multiplies nothing.
//
// MUTATION: drop fleet and the admin org and the hanzo bot are refused at 10.
func TestFirstPartyTrafficCarriesNoDefault(t *testing.T) {
	gp := staticPol(t, edge.Policy{OrgRPM: 10}) // default cap: 10 a minute
	send := fleetApp(t, gp)
	for i := range 30 {
		if code := send("admin", "person", "root"); code != http.StatusOK {
			t.Fatalf("a SuperAdmin in the admin org, request %d: %d", i+1, code)
		}
		if code := send("admin", "app", "app:admin/fleet-ops"); code != http.StatusOK {
			t.Fatalf("an admin-org application, request %d: %d", i+1, code)
		}
		if code := send("hanzo", "app", "app:hanzo/hanzo-bot"); code != http.StatusOK {
			t.Fatalf("a hanzo application, request %d: %d", i+1, code)
		}
	}
	for i := range 10 {
		if code := send("hanzo", "person", "hanzo/dana"); code != http.StatusOK {
			t.Fatalf("hanzo's people after its bot sent 30, request %d: %d — the bot spent their budget", i+1, code)
		}
	}
	if code := send("hanzo", "person", "hanzo/dana"); code != http.StatusTooManyRequests {
		t.Fatalf("hanzo's people past the default: %d, want 429", code)
	}
	for i := range 5 {
		send("acme", "app", fmt.Sprintf("app:acme/bot-%d", i))
	}
	for i := range 5 {
		if code := send("acme", "person", "acme/eve"); code != http.StatusOK {
			t.Fatalf("acme's people after five of its bots' requests, request %d: %d", i+1, code)
		}
	}
	if code := send("acme", "app", "app:acme/bot-new"); code != http.StatusTooManyRequests {
		t.Fatalf("a new acme application past the org's 10: %d, want 429 — its apps multiplied the cap", code)
	}
}

// A first-party org the platform pins is held at the pin, and its applications each
// in a window of their own.
func TestAPinnedFleetOrgIsHeldPerApplication(t *testing.T) {
	gp := orgPolicy(t, edge.Policy{OrgRPM: 10}, "hanzo", edge.Policy{OrgCap: 3})
	send := fleetApp(t, gp)
	for _, app := range []string{"app:hanzo/a", "app:hanzo/b"} {
		for i := range 3 {
			if code := send("hanzo", "app", app); code != http.StatusOK {
				t.Fatalf("%s request %d of its pinned 3: %d", app, i+1, code)
			}
		}
		if code := send("hanzo", "app", app); code != http.StatusTooManyRequests {
			t.Fatalf("%s past its pinned 3: %d, want 429", app, code)
		}
	}
}

// THE SIGNUP ORG IS NOT ONE TENANT. Every self-serve person lands in it and pays
// from an account of their own, so its default is counted per person, split as the
// wallets are: a stranger spending their own window refuses nobody else, and is
// still held to it.
//
// MUTATION: count the signup org in one org window and dana's first request is a
// 429.
func TestTheSignupOrgIsCountedPerPerson(t *testing.T) {
	gp := staticPol(t, edge.Policy{OrgRPM: 10}) // default cap: 10 a minute
	send := fleetApp(t, gp)
	org := account.SignupOrg
	for i := range 10 {
		if code := send(org, "person", org+"/mallory"); code != http.StatusOK {
			t.Fatalf("mallory request %d of her 10: %d", i+1, code)
		}
	}
	if code := send(org, "person", org+"/mallory"); code != http.StatusTooManyRequests {
		t.Fatalf("mallory past her own window: %d, want 429", code)
	}
	for i := range 10 {
		if code := send(org, "person", org+"/dana"); code != http.StatusOK {
			t.Fatalf("dana request %d after a stranger spent hers: %d — the signup org shares one window", i+1, code)
		}
	}
	if code := send(org, "key", org+"/erin"); code != http.StatusOK {
		t.Fatalf("a signup-org person on a key: %d", code)
	}
}

// A brand org is named verbatim: an application of an org spelled like a brand in
// another case is a tenant's, and carries the default.
func TestAFleetOrgIsNamedVerbatim(t *testing.T) {
	send := fleetApp(t, staticPol(t, edge.Policy{OrgRPM: 10})) // default cap: 10 a minute
	for _, org := range []string{"HANZO", "Hanzo"} {
		for range 10 {
			send(org, "app", "app:"+org+"/bot")
		}
		if code := send(org, "app", "app:"+org+"/bot"); code != http.StatusTooManyRequests {
			t.Fatalf("an application of %q past the default: %d, want 429 — it was read as a brand's", org, code)
		}
	}
}

// THE SIGNUP ORG'S FLOORS ARE PER PERSON TOO. Its whole ceiling is counted per
// person, and so is every narrower one: the audio floor (60 a minute) one stranger
// spends is theirs, not every self-serve person's.
//
// MUTATION: key the floor on the org and dana's first transcription is a 429.
func TestTheSignupOrgsFloorsArePerPerson(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: c.Header("X-User-Id"), Subject: c.Header("X-User-Id"), Kind: principal.Person})
		return c.Continue()
	}))
	app.Use(ScopeRateLimit(nil, staticPol(t, edge.Policy{})))
	app.Raw(http.MethodPost, "/v1/audio/transcriptions", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	send := func(user string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
		r.Header.Set("X-Org-Id", account.SignupOrg)
		r.Header.Set("X-User-Id", user)
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	floor := audioRPM(t)
	for i := range floor {
		if code := send(account.SignupOrg + "/mallory"); code != http.StatusOK {
			t.Fatalf("mallory's transcription %d of %d: %d", i+1, floor, code)
		}
	}
	if code := send(account.SignupOrg + "/mallory"); code != http.StatusTooManyRequests {
		t.Fatalf("mallory past her floor: %d, want 429", code)
	}
	if code := send(account.SignupOrg + "/dana"); code != http.StatusOK {
		t.Fatalf("dana's first transcription after a stranger spent hers: %d, want 200", code)
	}
}

func TestUnlimitedFirstPartyOperatorIsNeverThrottled(t *testing.T) {
	gp := staticPol(t, edge.Policy{OrgRPM: 5})
	send := fleetApp(t, gp)
	for i := range 25 {
		if code := send("hanzo", "person", "hanzo/z"); code != http.StatusOK {
			t.Fatalf("hanzo/z request %d: %d, want 200", i+1, code)
		}
	}
}
