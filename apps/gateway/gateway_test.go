package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// compose installs what a host installs. A subsystem never installs cloud.Bridge:
// the program's composer owns it — serve.go at the root of the fused host, the
// plugin constructor for a plugin program. In a test the test is the composer, so
// it owes the same install; skipping it drives a program where every org-scoped op
// answers 403 for a reason production callers never see.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountApp builds /v1/gateway over a real temp-dir store and returns both so a test
// can assert the HTTP surface and the persisted state.
func mountApp(t *testing.T) (*zip.App, *edge.Store) {
	t.Helper()
	st, err := edge.New(t.TempDir(), "admin", edge.Policy{PerIPRPM: 100, WindowSec: 1})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app := zip.New(zip.Config{})
	compose(app)
	if err := mount(app, cloud.Deps{Audit: trail}, st, nil); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	trails[app] = trail
	return app, st
}

// trails is the audit trail each mounted app writes to, for a test that reads it.
var trails = map[*zip.App]*audit.Recorder{}

// call drives a request with the given identity headers. There is no
// SanitizeIdentity in the test app, so the raw X-* headers stand in for a
// sanitized principal (X-User-Id ⇒ validated; X-User-IsAdmin=true ⇒ SuperAdmin).
func call(t *testing.T, app *zip.App, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// orgAdmin is an admin of org: the X-User-IsOrgAdmin bit the identity boundary
// mints from the signed membership role.
func orgAdmin(org string) map[string]string {
	return map[string]string{"X-User-Id": "u-" + org, "X-Org-Id": org, "X-User-IsOrgAdmin": "true"}
}

// member is a validated member of org who administers nothing.
func member(org string) map[string]string {
	return map[string]string{"X-User-Id": "m-" + org, "X-Org-Id": org}
}
func superAdmin(org string) map[string]string {
	return map[string]string{"X-User-Id": "root", "X-Org-Id": org, "X-User-IsAdmin": "true"}
}

func TestGet_RequiresPrincipal(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := call(t, app, http.MethodGet, "/v1/gateway/config", "", nil); code != 401 {
		t.Fatalf("no principal must be 401, got %d", code)
	}
}

func TestOrgAdmin_SetsOwnOrgRPM(t *testing.T) {
	app, st := mountApp(t)
	// An org admin sets its own ceiling.
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":60}`, orgAdmin("acme")); code != 200 {
		t.Fatalf("org OrgRPM write: got %d", code)
	}
	if got := st.OrgRPM("acme"); got != 60 {
		t.Fatalf("persisted OrgRPM(acme) = %d, want 60", got)
	}
	// GET reflects it in the effective view.
	code, body := call(t, app, http.MethodGet, "/v1/gateway/config", "", orgAdmin("acme"))
	if code != 200 {
		t.Fatalf("GET: %d", code)
	}
	var p edge.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if p.OrgRPM != 60 {
		t.Fatalf("effective OrgRPM = %d, want 60", p.OrgRPM)
	}
}

func TestOrgAdmin_CannotSetPlatform(t *testing.T) {
	app, st := mountApp(t)
	// A non-SuperAdmin PUT that carries a platform field is refused.
	code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"cors_origins":["https://x.io"]}`, orgAdmin("acme"))
	if code != 403 {
		t.Fatalf("org admin platform write must be 403, got %d", code)
	}
	if len(st.Platform().CORSOrigins) != 0 {
		t.Fatal("platform CORS must be untouched by a non-admin write")
	}
}

func TestSuperAdmin_SetsPlatform(t *testing.T) {
	app, st := mountApp(t)
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"per_ip_rpm":500}`, superAdmin("admin")); code != 200 {
		t.Fatalf("SuperAdmin platform write: got %d", code)
	}
	if got := st.Platform().PerIPRPM; got != 500 {
		t.Fatalf("platform PerIPRPM = %d, want 500", got)
	}
}

// The crux: a SuperAdmin viewing another tenant (org-switched X-Org-Id) still
// writes the PLATFORM row, not the switched tenant's row — PutPlatform targets the
// admin org explicitly.
func TestSuperAdmin_OrgSwitchedStillWritesPlatform(t *testing.T) {
	app, st := mountApp(t)
	code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"cors_origins":["https://y.io"]}`, superAdmin("some-tenant"))
	if code != 200 {
		t.Fatalf("write: %d", code)
	}
	if got := st.Platform().CORSOrigins; len(got) != 1 || got[0] != "https://y.io" {
		t.Fatalf("platform CORS = %v, want the org-switched SuperAdmin's write on the platform row", got)
	}
	// The switched tenant's own row must NOT have received the platform CORS.
	if raw, ok, _ := st.Get(t.Context(), "some-tenant"); ok && len(raw.CORSOrigins) != 0 {
		t.Fatalf("switched tenant row must not carry platform CORS, got %v", raw.CORSOrigins)
	}
}

func TestPut_EmptyBodyRejected(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{}`, orgAdmin("acme")); code != 400 {
		t.Fatalf("empty policy must be 400, got %d", code)
	}
}

// getPolicy GETs the effective config for the given identity and decodes it.
func getPolicy(t *testing.T, app *zip.App, hdr map[string]string) edge.Policy {
	t.Helper()
	code, body := call(t, app, http.MethodGet, "/v1/gateway/config", "", hdr)
	if code != 200 {
		t.Fatalf("GET: %d (%s)", code, body)
	}
	var p edge.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return p
}

// TestPerOrg_CacheRateCORS_RoundTripAndIsolation is the crux of the per-org edge
// config: an org PUTs cache TTL (default + per-path) + rate + method allowlist, and
// GET round-trips them merged onto the platform CORS. A second org sets its own and
// sees ONLY its own — the per-org rows are isolated, and the first org is untouched.
func TestPerOrg_CacheRateCORS_RoundTripAndIsolation(t *testing.T) {
	app, _ := mountApp(t)

	// Platform CORS (SuperAdmin) — the shared, org-independent edge knob every
	// tenant sees in its effective view.
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config",
		`{"cors_origins":["https://acme.example"]}`, superAdmin("admin")); code != 200 {
		t.Fatalf("platform CORS write: got %d (%s)", code, body)
	}

	// acme sets its full per-org config: rate + default/per-path cache + methods.
	acmeCfg := `{"org_rpm":120,"cache_ttl_sec":30,"cache_paths":{"/v1/models":300},"methods":["get","post"]}`
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config", acmeCfg, orgAdmin("acme")); code != 200 {
		t.Fatalf("acme per-org write: got %d (%s)", code, body)
	}

	// GET(acme) round-trips cache + rate + methods, plus the platform CORS.
	acme := getPolicy(t, app, orgAdmin("acme"))
	if acme.OrgRPM != 120 {
		t.Fatalf("acme OrgRPM = %d, want 120", acme.OrgRPM)
	}
	if acme.CacheTTLSec != 30 {
		t.Fatalf("acme CacheTTLSec = %d, want 30", acme.CacheTTLSec)
	}
	if acme.CachePaths["/v1/models"] != 300 {
		t.Fatalf("acme CachePaths = %v, want /v1/models:300", acme.CachePaths)
	}
	if len(acme.Methods) != 2 || acme.Methods[0] != "GET" || acme.Methods[1] != "POST" {
		t.Fatalf("acme Methods = %v, want [GET POST] (normalized)", acme.Methods)
	}
	if len(acme.CORSOrigins) != 1 || acme.CORSOrigins[0] != "https://acme.example" {
		t.Fatalf("acme CORS = %v, want the platform CORS", acme.CORSOrigins)
	}

	// globex sets a DIFFERENT config; GET(globex) sees only its own per-org values,
	// never acme's cache_paths/methods — but the same platform CORS.
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config",
		`{"org_rpm":10,"cache_ttl_sec":5}`, orgAdmin("globex")); code != 200 {
		t.Fatalf("globex per-org write: got %d (%s)", code, body)
	}
	globex := getPolicy(t, app, orgAdmin("globex"))
	if globex.OrgRPM != 10 || globex.CacheTTLSec != 5 {
		t.Fatalf("globex config = OrgRPM %d / CacheTTLSec %d, want 10/5", globex.OrgRPM, globex.CacheTTLSec)
	}
	if len(globex.CachePaths) != 0 {
		t.Fatalf("globex must NOT inherit acme's cache_paths, got %v", globex.CachePaths)
	}
	if len(globex.Methods) != 0 {
		t.Fatalf("globex must NOT inherit acme's methods, got %v", globex.Methods)
	}
	if len(globex.CORSOrigins) != 1 || globex.CORSOrigins[0] != "https://acme.example" {
		t.Fatalf("globex CORS = %v, want the shared platform CORS", globex.CORSOrigins)
	}

	// acme is unchanged by globex's write — isolation is symmetric.
	if acme2 := getPolicy(t, app, orgAdmin("acme")); acme2.OrgRPM != 120 || acme2.CacheTTLSec != 30 || len(acme2.Methods) != 2 {
		t.Fatalf("acme mutated by globex write: %+v", acme2)
	}
}

// TestPut_ValidatesPerOrgConfig: a structurally invalid per-org body is a 400, not
// a silently-dropped field.
func TestPut_ValidatesPerOrgConfig(t *testing.T) {
	app, _ := mountApp(t)
	for _, body := range []string{
		`{"methods":["TRACE"]}`,        // unknown HTTP method
		`{"cache_paths":{"v1":10}}`,    // path without leading /
		`{"cache_ttl_sec":9999999999}`, // over MaxCacheTTLSec
	} {
		if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", body, orgAdmin("acme")); code != 400 {
			t.Fatalf("invalid body %s must be 400, got %d", body, code)
		}
	}
}

// REGRESSION — the abuse gate must not be disarmable by the account it polices.
// Mode used to sit in the self-service branch, gated only on being a validated
// principal, so an org admin — or anyone holding an org-admin credential, which is
// what a stolen key buys — could PUT {"mode":"shadow"} and switch the control off
// for exactly the account it was watching. Setting it is the platform's decision
// now, whichever row it lands on.
func TestOrgAdmin_CannotSetItsOwnMode(t *testing.T) {
	app, st := mountApp(t)

	// Arm acme as the platform would.
	if _, err := st.Put(t.Context(), "acme", edge.Policy{Mode: edge.ModeLive}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if got := st.Mode("acme"); got != edge.ModeLive {
		t.Fatalf("precondition: acme = %q, want live", got)
	}

	// The subject of the control tries to turn it off, and then to turn it on.
	for _, body := range []string{`{"mode":"shadow"}`, `{"mode":"live"}`} {
		code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", body, orgAdmin("acme"))
		if code != 403 {
			t.Fatalf("org admin PUT %s must be 403, got %d (%s)", body, code, msg)
		}
	}
	if got := st.Mode("acme"); got != edge.ModeLive {
		t.Fatalf("acme disarmed itself: mode = %q", got)
	}

	// A per-org write that carries no mode still works — the refusal is about the
	// one field, not about the surface.
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":60}`, orgAdmin("acme")); code != 200 {
		t.Fatalf("an ordinary self-service write must still succeed, got %d", code)
	}
	if got := st.Mode("acme"); got != edge.ModeLive {
		t.Fatalf("an unrelated write changed the mode: %q", got)
	}
}

// A SuperAdmin sets the mode, on the tenant it names — the one authority that may.
// Guarded by the scorer check, so arming a deployment with no scorer installed is
// refused rather than turning every privileged grant into a 403.
func TestSuperAdmin_SetsModeOnATargetedTenant(t *testing.T) {
	app, st := mountApp(t)
	cloud.SetRiskScorer(func(ctx context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })

	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config?org=acme", `{"mode":"live"}`, superAdmin("admin")); code != 200 {
		t.Fatalf("SuperAdmin mode write: got %d (%s)", code, msg)
	}
	if got := st.Mode("acme"); got != edge.ModeLive {
		t.Fatalf("acme = %q after the operator armed it, want live", got)
	}
	// And only that tenant.
	if got := st.Mode("globex"); got != edge.ModeShadow {
		t.Fatalf("arming acme reached globex: %q", got)
	}
}

// Arming while no scorer is installed is refused: live makes a privileged grant
// fail CLOSED when the scorer cannot answer, which is right for a scorer that is
// momentarily down and an outage for one that was never there.
func TestSuperAdmin_CannotArmWithoutAScorer(t *testing.T) {
	app, st := mountApp(t)
	cloud.SetRiskScorer(nil)
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config?org=acme", `{"mode":"live"}`, superAdmin("admin")); code != 400 {
		t.Fatalf("arming with no scorer must be 400, got %d", code)
	}
	if got := st.Mode("acme"); got != edge.ModeShadow {
		t.Fatalf("acme armed with no scorer: %q", got)
	}
}

// The lane that has no tenant is where every caller the identity boundary could
// not validate is counted — the lane a bad bot calls from, and the one the
// platform row exists to arm. It had no spelling on this surface at all: the
// report could only be asked for by NAME, so its traffic and its saturation were
// readable by nobody, SuperAdmin included. The empty ?org= is its name.
func TestSuperAdmin_ReadsTheLaneWithNoTenant(t *testing.T) {
	st, err := edge.New(t.TempDir(), "admin", edge.Policy{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tr := edge.NewTraffic()
	app := zip.New(zip.Config{})
	compose(app)
	if err := mount(app, cloud.Deps{}, st, tr); err != nil {
		t.Fatalf("Use:  %v", err)
	}

	now := time.Now()
	for range 3 {
		tr.Observe(edge.Signal{Org: "", Presented: "junk", IP: "203.0.113.9", Path: "/v1/models", Class: edge.CredAnonymous}, now)
	}
	tr.Observe(edge.Signal{Org: "acme", Cred: "fp", Presented: "fp", IP: "198.51.100.1", Path: "/v1/models", Class: edge.CredSecret}, now)

	code, body := call(t, app, http.MethodGet, "/v1/gateway/traffic?org=", "", superAdmin("admin"))
	if code != 200 {
		t.Fatalf("GET traffic?org= → %d: %s", code, body)
	}
	var v edge.TrafficView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Org != "" || v.Requests != 3 {
		t.Fatalf("the anonymous lane's own view: %+v, want org=\"\" requests=3", v)
	}
	if v.Strain == "" || v.Ceiling == 0 {
		t.Fatalf("the lane must report what its ceilings are doing: %+v", v)
	}
	// It is a DIFFERENT scope from the admin org's own traffic, which is what a
	// reserved-word spelling would have conflated.
	code, body = call(t, app, http.MethodGet, "/v1/gateway/traffic?org=acme", "", superAdmin("admin"))
	if code != 200 {
		t.Fatalf("GET traffic?org=acme → %d: %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Org != "acme" || v.Requests != 1 {
		t.Fatalf("a named tenant's view: %+v, want org=acme requests=1", v)
	}
}

// And only a SuperAdmin may ask for it. An org admin asking for the lane with no
// tenant gets its OWN scope, exactly as it does for any other ?org= it is not
// entitled to.
func TestOrgAdmin_CannotReadTheLaneWithNoTenant(t *testing.T) {
	st, err := edge.New(t.TempDir(), "admin", edge.Policy{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tr := edge.NewTraffic()
	app := zip.New(zip.Config{})
	compose(app)
	if err := mount(app, cloud.Deps{}, st, tr); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	tr.Observe(edge.Signal{Org: "", Presented: "junk", IP: "203.0.113.9", Path: "/v1/models", Class: edge.CredAnonymous}, time.Now())

	code, body := call(t, app, http.MethodGet, "/v1/gateway/traffic?org=", "", orgAdmin("acme"))
	if code != 200 {
		t.Fatalf("GET traffic?org= as an org admin → %d: %s", code, body)
	}
	var v edge.TrafficView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Org != "acme" || v.Requests != 0 {
		t.Fatalf("an org admin read the anonymous lane: %+v", v)
	}
}

// THE ORG'S CEILING IS THE ORG ADMIN'S. org_rpm only ever tightens, which is what
// made it an attack while any validated member could write it: one PUT of
// {"org_rpm":1} throttled every caller the org has. A member — and an app acting
// as itself, which holds no admin scope either — is refused, and nothing it sent
// reaches the store.
//
// MUTATION: drop the IsOrgAdmin test from writer and the member writes.
func TestMember_CannotSetTheOrgsPolicy(t *testing.T) {
	app, st := mountApp(t)
	for _, body := range []string{`{"org_rpm":1}`, `{"cache_ttl_sec":5}`, `{"methods":["GET"]}`} {
		code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", body, member("acme"))
		if code != http.StatusForbidden || !strings.Contains(msg, "signed-in org admin") {
			t.Fatalf("a member PUT %s: %d %s, want 403 naming the org admin", body, code, msg)
		}
	}
	machine := map[string]string{"X-User-Id": "app-acme", "X-Org-Id": "acme", "X-User-IsApp": "true"}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":1}`, machine); code != http.StatusForbidden {
		t.Fatalf("an application acting as itself set the org's ceiling: %d %s", code, msg)
	}
	if got := st.OrgRPM("acme"); got != 0 {
		t.Fatalf("a refused write reached the store: OrgRPM(acme) = %d", got)
	}
	// Reading is not writing: the member still sees the policy it is subject to —
	// the ceiling in force, which is the org's cap.
	if p := getPolicy(t, app, member("acme")); p.OrgRPM != st.Cap("acme") || p.OrgCap != st.Cap("acme") {
		t.Fatalf("member read: %+v", p)
	}
}

// NO API KEY WRITES THE ORG'S POLICY. A key arrives with no signed membership set,
// so the boundary mints neither admin bit for it — not for an unrestricted key, not
// for one whose holder administers the org (TestAKeyIsNeverAnAdmin, in package
// cloud, drives that through the boundary). Here the key arrives as the boundary
// delivers it, and is refused whatever it may reach.
func TestAnAPIKey_CannotSetTheOrgsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit []string
	}{
		{"an unrestricted key", nil},
		{"a key reaching only the gateway", []string{"product:gateway"}},
		{"a key held to a rate", []string{"rate:minute:60"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := edge.New(t.TempDir(), "admin", edge.Policy{PerIPRPM: 100, WindowSec: 1})
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			app := zip.New(zip.Config{})
			// The boundary, attesting the key the request arrived on.
			app.Use(zip.H(func(c *zip.Ctx) error {
				principal.Mint(c, principal.Principal{Org: "acme", User: "acme/alice", Subject: "acme/alice",
					Kind: principal.Key, Key: "k1", Holder: "acme/alice", Limit: tc.limit})
				return c.Continue()
			}))
			compose(app)
			if err := mount(app, cloud.Deps{}, st, nil); err != nil {
				t.Fatalf("Use: %v", err)
			}
			hdr := map[string]string{"X-User-Id": "acme/alice", "X-Org-Id": "acme", "Authorization": "Bearer sk-acme-alice"}
			code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":1}`, hdr)
			if code != http.StatusForbidden || !strings.Contains(msg, "signed-in org admin") {
				t.Fatalf("PUT org_rpm on %s: %d %s, want 403 naming who may", tc.name, code, msg)
			}
			if got := st.OrgRPM("acme"); got != 0 {
				t.Fatalf("a key's write reached the store: OrgRPM(acme) = %d", got)
			}
		})
	}
}

// A BROWSER SESSION'S CHANGE IS MADE ON PURPOSE. A session cookie rides any request
// a page is made to send, so an org admin's cookie alone does not set the org's
// ceiling: the browser must say the change came from the page's own origin
// (cloud.Intended). A presented bearer is not ambient and is asked nothing.
func TestBrowserSession_SetsTheOrgsPolicyOnlyOnPurpose(t *testing.T) {
	app, st := mountApp(t)
	session := func(set map[string]string) map[string]string {
		h := orgAdmin("acme")
		h["Cookie"] = "hanzo_iam_token=eyJhbGciOiJSUzI1NiJ9.e30.sig"
		for k, v := range set {
			h[k] = v
		}
		return h
	}
	for _, tc := range []struct {
		name string
		hdr  map[string]string
	}{
		{"a cookie that states no origin", session(nil)},
		{"a sibling site", session(map[string]string{"Sec-Fetch-Site": "same-site"})},
		{"another site", session(map[string]string{"Sec-Fetch-Site": "cross-site"})},
	} {
		code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":1}`, tc.hdr)
		if code != http.StatusForbidden || !strings.Contains(msg, cloud.Unasked) {
			t.Fatalf("%s: %d %s, want 403 %q", tc.name, code, msg, cloud.Unasked)
		}
	}
	if got := st.OrgRPM("acme"); got != 0 {
		t.Fatalf("a forged change reached the store: OrgRPM(acme) = %d", got)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":90}`, session(map[string]string{"Sec-Fetch-Site": "same-origin"})); code != http.StatusOK {
		t.Fatalf("the console's own change: %d %s", code, msg)
	}
	if got := st.OrgRPM("acme"); got != 90 {
		t.Fatalf("OrgRPM(acme) = %d, want 90", got)
	}
	// The SuperAdmin is asked the same question, and passes it the same way.
	root := superAdmin("admin")
	root["Cookie"] = "hanzo_iam_token=eyJhbGciOiJSUzI1NiJ9.e30.sig"
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"per_ip_rpm":7}`, root); code != http.StatusForbidden {
		t.Fatalf("a SuperAdmin cookie with no stated origin set the platform cap: %d", code)
	}
	if got := st.Platform().PerIPRPM; got != 100 {
		t.Fatalf("platform PerIPRPM = %d, want the boot 100", got)
	}
}

// AN ORGANIZATION LOWERS ITS CEILING AND NEVER RAISES IT PAST ITS CAP. Every free
// signup administers an org of its own, so a ceiling its admin could raise would be
// no ceiling: org_rpm above the cap (the platform default — here 100 a second as a
// minute, times ten) is refused, and org_cap is the platform's field, like mode.
// Only a SuperAdmin raises an org's cap; inside it, the org's admin sets org_rpm.
func TestAnOrgsCapIsThePlatforms(t *testing.T) {
	app, st := mountApp(t)
	defaultCap := st.Cap("acme")
	if defaultCap != 60000 {
		t.Fatalf("Cap(acme) = %d, want 60000 (100/s × 10, as a minute)", defaultCap)
	}
	above := fmt.Sprintf(`{"org_rpm":%d}`, defaultCap+1)
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", above, orgAdmin("acme")); code != http.StatusForbidden || !strings.Contains(msg, "org_cap") {
		t.Fatalf("an org admin raising org_rpm past the cap: %d %s, want 403 naming org_cap", code, msg)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", fmt.Sprintf(`{"org_rpm":%d}`, defaultCap), orgAdmin("acme")); code != http.StatusOK {
		t.Fatalf("an org admin setting org_rpm at the cap: %d %s", code, msg)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_cap":100000}`, orgAdmin("acme")); code != http.StatusForbidden {
		t.Fatalf("an org admin raising its own cap: %d %s, want 403", code, msg)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config?org=acme", `{"org_cap":100000}`, superAdmin("admin")); code != http.StatusOK {
		t.Fatalf("a SuperAdmin raising acme's cap: %d %s", code, msg)
	}
	if got := st.Cap("acme"); got != 100000 {
		t.Fatalf("Cap(acme) after the raise = %d, want 100000", got)
	}
	if got := st.Cap("globex"); got != defaultCap {
		t.Fatalf("raising acme's cap reached globex: %d", got)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":90000}`, orgAdmin("acme")); code != http.StatusOK {
		t.Fatalf("an org admin raising org_rpm inside its raised cap: %d %s", code, msg)
	}
	if p := getPolicy(t, app, orgAdmin("acme")); p.OrgCap != 100000 || p.OrgRPM != 90000 {
		t.Fatalf("acme's effective policy: org_cap %d org_rpm %d, want 100000 and 90000", p.OrgCap, p.OrgRPM)
	}
}

// RAISING A CAP IS ON THE TRAIL, OR IT DOES NOT HAPPEN. A SuperAdmin's org_cap write
// lifts a tenant's ceiling, so it is recorded — who, which org, the cap before and
// after — before it is made, and a deployment whose trail cannot take it refuses
// the change.
//
// MUTATION: skip audited and the trail has no gateway.org_cap row.
func TestARaisedCapIsAudited(t *testing.T) {
	app, _ := mountApp(t)
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config?org=acme", `{"org_cap":100000}`, superAdmin("admin")); code != http.StatusOK {
		t.Fatalf("a SuperAdmin raising acme's cap: %d %s", code, msg)
	}
	rows, _, err := trails[app].Query(t.Context(), audit.Filter{Action: "gateway.org_cap"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("gateway.org_cap rows: %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Resource.ID != "acme" || r.Actor.Sub != "root" || !strings.Contains(string(r.Before), `"org_cap":0`) || !strings.Contains(string(r.After), `"org_cap":100000`) {
		t.Fatalf("the row does not say who raised which cap from what to what: %+v before=%s after=%s", r, r.Before, r.After)
	}

	// No trail, no change.
	st2, err := edge.New(t.TempDir(), "admin", edge.Policy{PerIPRPM: 100, WindowSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	bare := zip.New(zip.Config{})
	compose(bare)
	if err := mount(bare, cloud.Deps{}, st2, nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, bare, http.MethodPut, "/v1/gateway/config?org=acme", `{"org_cap":100000}`, superAdmin("admin")); code != http.StatusServiceUnavailable {
		t.Fatalf("an unaudited cap raise: %d, want 503", code)
	}
	if got := st2.OwnCap("acme"); got != 0 {
		t.Fatalf("an unaudited cap raise reached the store: %d", got)
	}
}

// THE DEFAULT CAP IS ON THE TRAIL TOO. The platform's org_rpm is the cap of every
// org that has none of its own, so moving it lifts or lowers every tenant at once.
// It is recorded with its value before and after whichever way it is written — on
// the admin org's row, which IS the platform row, or beside a platform field — and
// a trail that cannot take it refuses the change.
//
// MUTATION: skip the gateway.default_cap record on either path and its row is missing.
func TestAMovedDefaultCapIsAudited(t *testing.T) {
	app, st := mountApp(t)
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":70000}`, superAdmin("admin")); code != http.StatusOK {
		t.Fatalf("a SuperAdmin setting the admin row's org_rpm: %d %s", code, msg)
	}
	if code, msg := call(t, app, http.MethodPut, "/v1/gateway/config", `{"per_ip_rpm":100,"org_rpm":80000}`, superAdmin("admin")); code != http.StatusOK {
		t.Fatalf("a SuperAdmin platform write carrying org_rpm: %d %s", code, msg)
	}
	if got := st.Cap("acme"); got != 80000 {
		t.Fatalf("Cap(acme) after the default moved = %d, want 80000", got)
	}
	rows, _, err := trails[app].Query(t.Context(), audit.Filter{Action: "gateway.default_cap"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("gateway.default_cap rows: %d, want 2", len(rows))
	}
	steps := map[string]bool{}
	for _, r := range rows {
		if r.Resource.ID != "admin" || r.Actor.Sub != "root" {
			t.Fatalf("the row does not say who moved the default cap: %+v", r)
		}
		steps[string(r.Before)+" -> "+string(r.After)] = true
	}
	if !steps[`{"org_rpm":0} -> {"org_rpm":70000}`] || !steps[`{"org_rpm":70000} -> {"org_rpm":80000}`] {
		t.Fatalf("the rows do not say what the default cap moved from and to: %v", steps)
	}

	// No trail, no change, on either path.
	st2, err := edge.New(t.TempDir(), "admin", edge.Policy{PerIPRPM: 100, WindowSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	bare := zip.New(zip.Config{})
	compose(bare)
	if err := mount(bare, cloud.Deps{}, st2, nil); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"org_rpm":70000}`, `{"per_ip_rpm":100,"org_rpm":70000}`} {
		if code, _ := call(t, bare, http.MethodPut, "/v1/gateway/config", body, superAdmin("admin")); code != http.StatusServiceUnavailable {
			t.Fatalf("an unaudited default cap move %s: %d, want 503", body, code)
		}
	}
	if got := st2.Platform().OrgRPM; got != 0 {
		t.Fatalf("an unaudited default cap move reached the store: %d", got)
	}
}
