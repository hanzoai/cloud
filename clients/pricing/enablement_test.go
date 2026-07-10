package pricing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// TestEnablement_TriStateVisibility pins the resolver's security invariant: ga is
// visible to everyone, off is visible to NO ONE (even an org sitting in betaOrgs),
// and beta is visible only to an org on the list. This is the ONE gate every
// surface shares — the off kill switch must be absolute.
func TestEnablement_TriStateVisibility(t *testing.T) {
	cases := []struct {
		name    string
		o       Overlay
		org     string
		visible bool
	}{
		{"ga visible to anyone", Overlay{Enabled: true}, "acme", true},
		{"ga visible to anon", Overlay{Enabled: true}, "", true},
		{"off hidden from all", Overlay{Enabled: false, Beta: false}, "acme", false},
		{"off hidden even with a stray beta org", Overlay{Enabled: false, Beta: false, BetaOrgs: []string{"acme"}}, "acme", false},
		{"beta visible to opted-in org", Overlay{Enabled: false, Beta: true, BetaOrgs: []string{"acme"}}, "acme", true},
		{"beta hidden from other org", Overlay{Enabled: false, Beta: true, BetaOrgs: []string{"acme"}}, "other", false},
		{"beta hidden from anon", Overlay{Enabled: false, Beta: true, BetaOrgs: []string{"acme"}}, "", false},
	}
	for _, tc := range cases {
		if got := tc.o.visibleTo(tc.org); got != tc.visible {
			t.Errorf("%s: visibleTo(%q)=%v, want %v", tc.name, tc.org, got, tc.visible)
		}
	}
	// State() derivation.
	if (Overlay{Enabled: true}).State() != "ga" {
		t.Error("enabled → ga")
	}
	if (Overlay{Enabled: false, Beta: true}).State() != "beta" {
		t.Error("!enabled+beta → beta")
	}
	if (Overlay{Enabled: false, Beta: false}).State() != "off" {
		t.Error("!enabled+!beta → off")
	}
}

// TestEnablement_OptInRefusesNonBeta proves the store's self-opt-in can NEVER
// bypass an off kill switch or touch a ga item — it only ever adds an org to a
// BETA item's list.
func TestEnablement_OptInRefusesNonBeta(t *testing.T) {
	c, err := openCatalog(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	// off item: opt-in must be refused, and the org must NOT gain visibility.
	_ = c.Upsert(ctx, Overlay{Kind: "feature", ID: "kill", Enabled: false, Beta: false})
	if _, err := c.OptIn(ctx, "feature", "kill", "acme"); err != errNotBeta {
		t.Fatalf("opt-in into off item must be errNotBeta, got %v", err)
	}
	o, _, _ := c.Get(ctx, "feature", "kill")
	if o.visibleTo("acme") {
		t.Fatal("SECURITY: an off item became visible after a refused opt-in")
	}

	// ga item: opt-in refused (already visible; no beta to join).
	_ = c.Upsert(ctx, Overlay{Kind: "feature", ID: "public", Enabled: true})
	if _, err := c.OptIn(ctx, "feature", "public", "acme"); err != errNotBeta {
		t.Fatalf("opt-in into ga item must be errNotBeta, got %v", err)
	}

	// beta item: opt-in succeeds and grants the caller's org visibility only.
	_ = c.Upsert(ctx, Overlay{Kind: "feature", ID: "labs", Enabled: false, Beta: true})
	if _, err := c.OptIn(ctx, "feature", "labs", "acme"); err != nil {
		t.Fatalf("opt-in into beta must succeed, got %v", err)
	}
	o, _, _ = c.Get(ctx, "feature", "labs")
	if !o.visibleTo("acme") {
		t.Error("acme opted in but cannot see the beta")
	}
	if o.visibleTo("other") {
		t.Error("opt-in of acme must NOT expose the beta to another org")
	}
	// opt-out reverses it.
	if _, err := c.OptIn(ctx, "feature", "labs", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OptOut(ctx, "feature", "labs", "acme"); err != nil {
		t.Fatal(err)
	}
	o, _, _ = c.Get(ctx, "feature", "labs")
	if o.visibleTo("acme") {
		t.Error("acme opted out but still sees the beta")
	}
}

// mountEnablement spins up the real pricing subsystem for HTTP enablement tests.
func mountEnablement(t *testing.T) func(method, path, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo", DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	fa := app.Fiber()
	return func(method, path, body string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
}

// TestEnablement_GlobalSetIsAdminOnly proves a customer/org-admin can NEVER change
// global enablement state — the admin registry surface is global-admin only.
func TestEnablement_GlobalSetIsAdminOnly(t *testing.T) {
	do := mountEnablement(t)
	cust := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"} // org-level, NOT global admin

	// A customer cannot LIST the global registry.
	if resp, _ := do("GET", "/v1/admin/enablement", "", cust); resp.StatusCode != http.StatusForbidden {
		t.Errorf("customer GET /v1/admin/enablement = %d, want 403", resp.StatusCode)
	}
	// A customer cannot SET global state (even forging X-Org-Id: admin, which the
	// gateway strips — here no X-User-IsAdmin means not global).
	body := `{"kind":"feature","id":"labs","state":"ga"}`
	if resp, _ := do("PUT", "/v1/admin/enablement", body, cust); resp.StatusCode != http.StatusForbidden {
		t.Errorf("customer PUT /v1/admin/enablement = %d, want 403", resp.StatusCode)
	}
	// Even a forged X-User-IsAdmin from a customer is moot here because the gateway
	// mints it only for owner==admin; simulate that the customer canNOT set it by
	// confirming the state never changed: an admin list shows no 'labs' row.
	admin := map[string]string{"X-User-IsAdmin": "true"}
	_, lb := do("GET", "/v1/admin/enablement", "", admin)
	if strings.Contains(string(lb), `"id":"labs"`) {
		t.Error("a customer PUT must NOT have created the labs item")
	}
}

// TestEnablement_FullFlow is the coordinator's live verify, end to end over HTTP:
// admin sets a model beta → a normal org does NOT see it → the org opts in → it
// NOW sees it (gated /v1/pricing/models proves the live gating) → admin sets ga →
// everyone sees → admin sets off → nobody sees, even the opted-in org.
func TestEnablement_FullFlow(t *testing.T) {
	do := mountEnablement(t)
	admin := map[string]string{"X-User-IsAdmin": "true"}
	acme := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}
	other := map[string]string{"X-Org-Id": "other", "X-User-Id": "u_other"}

	const model = "anthropic/claude-opus-4.6" // a real bundle model id (see admin_http_test)

	sees := func(hdr map[string]string) bool {
		_, b := do("GET", "/v1/pricing/models", "", hdr)
		var wrap struct {
			Models []map[string]any `json:"models"`
		}
		_ = json.Unmarshal(b, &wrap)
		for _, m := range wrap.Models {
			if str(m["id"]) == model || str(m["name"]) == model {
				return true
			}
		}
		return false
	}

	// Baseline: everyone sees it (untouched → ga).
	if !sees(acme) || !sees(other) {
		t.Fatalf("baseline: both orgs must see the model (acme=%v other=%v)", sees(acme), sees(other))
	}

	// Admin sets it to BETA.
	if resp, b := do("PUT", "/v1/admin/enablement", `{"kind":"model","id":"`+model+`","state":"beta"}`, admin); resp.StatusCode != 200 {
		t.Fatalf("admin set beta: %d (%s)", resp.StatusCode, b)
	}
	// Now NEITHER org sees it (beta, nobody opted in).
	if sees(acme) || sees(other) {
		t.Fatalf("after beta: no org should see it yet (acme=%v other=%v)", sees(acme), sees(other))
	}
	// acme sees it in its enablement view as a beta it can opt into.
	_, ev := do("GET", "/v1/enablement", "", acme)
	if !strings.Contains(string(ev), `"canOptIn":true`) {
		t.Errorf("acme /v1/enablement must offer the beta to opt into: %s", ev)
	}

	// acme OPTS IN.
	if resp, b := do("POST", "/v1/enablement/optin", `{"kind":"model","id":"`+model+`"}`, acme); resp.StatusCode != 200 {
		t.Fatalf("acme opt-in: %d (%s)", resp.StatusCode, b)
	}
	// acme NOW sees it; other STILL does not (per-org isolation).
	if !sees(acme) {
		t.Error("after opt-in, acme must see the beta model in /v1/pricing/models")
	}
	if sees(other) {
		t.Error("other must NOT see acme's opted-in beta")
	}

	// Admin sets GA → everyone sees.
	do("PUT", "/v1/admin/enablement", `{"kind":"model","id":"`+model+`","state":"ga"}`, admin)
	if !sees(acme) || !sees(other) {
		t.Errorf("after ga: everyone must see it (acme=%v other=%v)", sees(acme), sees(other))
	}

	// Admin sets OFF → nobody sees, even the opted-in acme.
	do("PUT", "/v1/admin/enablement", `{"kind":"model","id":"`+model+`","state":"off"}`, admin)
	if sees(acme) || sees(other) {
		t.Errorf("after off: NOBODY may see it, even opted-in acme (acme=%v other=%v)", sees(acme), sees(other))
	}
}

// TestEnablement_OptInScopedToCaller proves a self-opt-in can only ever enable the
// SANITIZED caller's own org — a different caller does not gain access, and there
// is no body field that lets a caller target another org.
func TestEnablement_OptInScopedToCaller(t *testing.T) {
	do := mountEnablement(t)
	admin := map[string]string{"X-User-IsAdmin": "true"}
	acme := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}

	do("PUT", "/v1/admin/enablement", `{"kind":"feature","id":"labs","state":"beta"}`, admin)

	// An unauthenticated caller (no org) cannot opt in.
	if resp, _ := do("POST", "/v1/enablement/optin", `{"kind":"feature","id":"labs"}`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon opt-in = %d, want 401", resp.StatusCode)
	}

	// acme opts in — even if the body tried to name another org, only c.Org() counts
	// (there is no org field; the subject is the sanitized header).
	do("POST", "/v1/enablement/optin", `{"kind":"feature","id":"labs","org":"victim"}`, acme)

	// The admin registry shows ONLY acme granted — never "victim".
	_, lb := do("GET", "/v1/admin/enablement", "", admin)
	if !strings.Contains(string(lb), `"acme"`) {
		t.Errorf("acme must be the granted org: %s", lb)
	}
	if strings.Contains(string(lb), `"victim"`) {
		t.Fatal("SECURITY: a caller forged an opt-in for another org (victim)")
	}
}

// TestEnablement_CannotOptIntoOff proves a self-opt-in is refused for an off item
// (400), and the off item stays invisible — the kill switch holds against opt-in.
func TestEnablement_CannotOptIntoOff(t *testing.T) {
	do := mountEnablement(t)
	admin := map[string]string{"X-User-IsAdmin": "true"}
	acme := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}

	do("PUT", "/v1/admin/enablement", `{"kind":"feature","id":"kill","state":"off"}`, admin)
	if resp, _ := do("POST", "/v1/enablement/optin", `{"kind":"feature","id":"kill"}`, acme); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("opt-in into off = %d, want 400", resp.StatusCode)
	}
	// acme's enablement view must show it NOT effective and NOT opt-in-able.
	_, ev := do("GET", "/v1/enablement", "", acme)
	if strings.Contains(string(ev), `"id":"kill","state":"off","effective":true`) {
		t.Error("an off item must never be effective for a customer")
	}
}
