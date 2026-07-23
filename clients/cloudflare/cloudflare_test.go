package cloudflare

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// testAccountID is a valid 32-hex Cloudflare account id the stub discovers.
const testAccountID = "0123456789abcdef0123456789abcdef"

// capture records every request the fake Cloudflare API received, so a test can
// assert WHICH token (Authorization) and WHICH path a handler used.
type capture struct {
	mu   sync.Mutex
	reqs []capturedReq
}

type capturedReq struct {
	method, path, query, auth, ctype string
	body                             []byte
}

func (c *capture) add(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.reqs = append(c.reqs, capturedReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
	c.mu.Unlock()
}

// find returns the first captured request whose path CONTAINS sub.
func (c *capture) find(sub string) (capturedReq, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reqs {
		if strings.Contains(r.path, sub) {
			return r, true
		}
	}
	return capturedReq{}, false
}

// hasExact reports whether any captured request hit EXACTLY path p — used to detect
// the account-discovery call (GET /accounts), which "/accounts/{id}/..." contains as
// a substring but is not.
func (c *capture) hasExact(p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reqs {
		if r.path == p {
			return true
		}
	}
	return false
}

// fakeCF is a minimal Cloudflare API v4 stub: it answers account discovery and
// echoes a success envelope for any account/zone-scoped call, recording every
// request. resultFor lets a test control the result body per path substring.
func fakeCF(rec *capture, resultFor func(path string) (int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/accounts" { // discovery
			io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"`+testAccountID+`","name":"acme"}]}`)
			return
		}
		if resultFor != nil {
			if status, body := resultFor(r.URL.Path); body != "" {
				w.WriteHeader(status)
				io.WriteString(w, body)
				return
			}
		}
		io.WriteString(w, `{"success":true,"errors":[],"result":{"ok":true}}`)
	}
}

// harness mounts the subsystem against a fake CF and per-org token seam.
func harness(t *testing.T, tokens map[string]string, rec *capture, resultFor func(string) (int, string)) *zip.App {
	t.Helper()
	srv := httptest.NewServer(fakeCF(rec, resultFor))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_API_BASE", srv.URL)

	prev := tokenFor
	tokenFor = func(_ context.Context, org, provider, name string) ([]byte, error) {
		if provider != providerCloudflare || name != secretAPIToken {
			return nil, fmt.Errorf("seam called with unexpected coordinate %s/%s", provider, name)
		}
		tok, ok := tokens[org]
		if !ok {
			return nil, fmt.Errorf("cloudflare not connected for org %q", org)
		}
		return []byte(tok), nil
	}
	t.Cleanup(func() { tokenFor = prev })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// doReq drives one request with the given minted identity headers (as
// SanitizeIdentity would set them): user!="" makes a validated principal; admin sets
// X-User-IsOrgAdmin. Returns status + body + response headers.
func doReq(t *testing.T, app *zip.App, method, path, user, org string, admin bool, body string) (int, string, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// do is the common non-admin read/driver: validated principal, no admin bit.
func do(t *testing.T, app *zip.App, method, path, user, org, body string) (int, string) {
	status, b, _ := doReq(t, app, method, path, user, org, false, body)
	return status, b
}

// ── tenant isolation (the crown jewel) ──────────────────────────────────────────

// A request with no validated principal (no X-User-Id) is refused 403 and NEVER
// reaches token custody or Cloudflare — the forged-X-Org-Id path is dead.
func TestForgedOrgWithoutPrincipalIs403(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"victim": "tok-victim"}, rec, nil)

	status, body := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "", "victim", "")
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", status, body)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("Cloudflare was contacted %d time(s) for an unvalidated request; must be 0", len(rec.reqs))
	}
	if strings.Contains(body, "tok-victim") {
		t.Fatalf("victim token leaked into response: %s", body)
	}
}

// Each org's request uses ONLY its own org's token; there is no input by which one
// org's request can carry another org's token (org is derived solely from the
// validated principal, never from a body/query field).
func TestTokenScopedToCallerOrg(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A", "orgb": "tok-B"}, rec, nil)

	if status, body := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ua", "orga", ""); status != 200 {
		t.Fatalf("orgA status=%d body=%s", status, body)
	}
	rA, ok := rec.find("/pages/projects")
	if !ok {
		t.Fatal("orgA: no /pages/projects request reached Cloudflare")
	}
	if rA.auth != "Bearer tok-A" {
		t.Fatalf("orgA used %q, want Bearer tok-A (cross-org token reach!)", rA.auth)
	}

	rec.reqs = nil
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ub", "orgb", ""); status != 200 {
		t.Fatalf("orgB status=%d", status)
	}
	rB, ok := rec.find("/pages/projects")
	if !ok {
		t.Fatal("orgB: no /pages/projects request reached Cloudflare")
	}
	if rB.auth != "Bearer tok-B" {
		t.Fatalf("orgB used %q, want Bearer tok-B", rB.auth)
	}
	if rB.auth == "Bearer tok-A" {
		t.Fatal("orgB reached orgA's token — cross-tenant break")
	}
}

// A body/query field naming another org is IGNORED: the token is the caller-org's,
// proving the handler derives the org only from the validated principal. (Mutation →
// org-admin driver.)
func TestBodyOrgFieldCannotRedirectToken(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A", "orgb": "tok-B"}, rec, nil)

	body := `{"name":"site","org":"orgb","organizationId":"orgb","account":"ffffffffffffffffffffffffffffffff"}`
	if status, resp, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/pages/projects", "ua", "orga", true, body); status != 200 {
		t.Fatalf("status=%d resp=%s", status, resp)
	}
	r, ok := rec.find("/pages/projects")
	if !ok {
		t.Fatal("no create request reached Cloudflare")
	}
	if r.auth != "Bearer tok-A" {
		t.Fatalf("hostile body redirected the token to %q; must stay Bearer tok-A", r.auth)
	}
}

// An org that has not connected Cloudflare fails closed with 503, never another
// org's data and never a fake success.
func TestNotConnectedIs503(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	status, body := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ux", "stranger", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", status, body)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("Cloudflare contacted for an unconnected org; must be 0 (got %d)", len(rec.reqs))
	}
}

// Every served response stamps X-Hanzo-Org with the org whose token was used, so a
// per-org caller can detect a pinned/comingled org (fix for the non-SuperAdmin
// platform-token misdeploy).
func TestResponseStampsActingOrg(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	status, _, hdr := doReq(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ua", "orga", false, "")
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if got := hdr.Get(actingOrgHeader); got != "orga" {
		t.Fatalf("%s = %q, want orga", actingOrgHeader, got)
	}
}

// ── mutation authorization (least privilege) ────────────────────────────────────

// Mutations (POST/PUT/DELETE) require org admin; reads do not. A refused non-admin
// mutation never reaches Cloudflare (rejected before the token read).
func TestMutationRequiresOrgAdmin(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)

	mutations := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cloudflare/pages/projects", `{"name":"site"}`},
		{http.MethodDelete, "/v1/cloudflare/pages/projects/site", ""},
		{http.MethodPost, "/v1/cloudflare/pages/projects/site/deployments", ""},
		{http.MethodPut, "/v1/cloudflare/workers/scripts/hello", `{"script":"export default {}"}`},
		{http.MethodDelete, "/v1/cloudflare/workers/scripts/hello", ""},
		{http.MethodPost, "/v1/cloudflare/r2/buckets", `{"name":"b"}`},
	}
	for _, m := range mutations {
		if s, _, _ := doReq(t, app, m.method, m.path, "member", "orga", false, m.body); s != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: status=%d, want 403", m.method, m.path, s)
		}
	}
	// A refused non-admin mutation must not have reached Cloudflare at all.
	if len(rec.reqs) != 0 {
		t.Fatalf("refused non-admin mutations reached Cloudflare %d time(s); must be 0", len(rec.reqs))
	}

	// An org ADMIN is allowed through to Cloudflare.
	if s, b, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/pages/projects", "admin", "orga", true, `{"name":"site"}`); s != 200 {
		t.Fatalf("org-admin create: status=%d body=%s, want 200", s, b)
	}
	// A read stays open to a non-admin member.
	if s, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "member", "orga", ""); s != 200 {
		t.Fatalf("non-admin read: status=%d, want 200", s)
	}
}

// ── wired behavior ──────────────────────────────────────────────────────────────

func TestPagesListHappyPath(t *testing.T) {
	rec := &capture{}
	resultFor := func(path string) (int, string) {
		if strings.HasSuffix(path, "/pages/projects") {
			return 200, `{"success":true,"errors":[],"result":[{"id":"p1","name":"marketing"}]}`
		}
		return 0, ""
	}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, resultFor)
	status, body := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ua", "orga", "")
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, `"name":"marketing"`) {
		t.Fatalf("result not relayed verbatim: %s", body)
	}
	r, _ := rec.find("/pages/projects")
	if r.path != "/accounts/"+testAccountID+"/pages/projects" {
		t.Fatalf("addressed %q, want the resolved-account path", r.path)
	}
	if strings.Contains(body, "tok-A") {
		t.Fatalf("token leaked into response body: %s", body)
	}
}

// Worker script PUT sends the modern multipart module upload with the caller-org
// token (org-admin driver).
func TestWorkersScriptPutMultipart(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	body := `{"script":"export default { fetch(){ return new Response('hi') } }","mainModule":"worker.js"}`
	status, resp, _ := doReq(t, app, http.MethodPut, "/v1/cloudflare/workers/scripts/hello", "ua", "orga", true, body)
	if status != 200 {
		t.Fatalf("status=%d resp=%s", status, resp)
	}
	r, ok := rec.find("/workers/scripts/hello")
	if !ok {
		t.Fatal("no script PUT reached Cloudflare")
	}
	if !strings.HasPrefix(r.ctype, "multipart/form-data") {
		t.Fatalf("content-type = %q, want multipart/form-data", r.ctype)
	}
	if !strings.Contains(string(r.body), `"main_module":"worker.js"`) {
		t.Fatalf("multipart metadata missing main_module: %s", r.body)
	}
	if !strings.Contains(string(r.body), "export default") {
		t.Fatal("multipart body missing the module source")
	}
	if r.auth != "Bearer tok-A" {
		t.Fatalf("script PUT used %q, want Bearer tok-A", r.auth)
	}
}

// Zone routes are zone-scoped and do NOT resolve an account (org-admin driver).
func TestWorkersRouteBindZoneScoped(t *testing.T) {
	rec := &capture{}
	zone := "abcdef0123456789abcdef0123456789"
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	body := `{"pattern":"example.com/*","script":"hello"}`
	status, resp, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/workers/zones/"+zone+"/routes", "ua", "orga", true, body)
	if status != 200 {
		t.Fatalf("status=%d resp=%s", status, resp)
	}
	if r, ok := rec.find("/zones/" + zone + "/workers/routes"); !ok {
		t.Fatalf("route bind did not address the zone path; got %+v", rec.reqs)
	} else if r.auth != "Bearer tok-A" {
		t.Fatalf("route bind used %q, want Bearer tok-A", r.auth)
	}
	if rec.hasExact("/accounts") {
		t.Fatal("route bind resolved an account; zone routes must not")
	}
}

// ── R2 / KV / D1 (wired) ────────────────────────────────────────────────────────

// The R2/KV/D1 LIST reads RELAY Cloudflare (never a 501), addressing the correct
// account-scoped upstream path with the caller-org token, and still gate on a
// validated principal.
func TestR2KVD1ListRelaysAndGates(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, tc := range []struct{ path, upstream string }{
		{"/v1/cloudflare/r2/buckets", "/accounts/" + testAccountID + "/r2/buckets"},
		{"/v1/cloudflare/kv/namespaces", "/accounts/" + testAccountID + "/storage/kv/namespaces"},
		{"/v1/cloudflare/d1/databases", "/accounts/" + testAccountID + "/d1/database"},
	} {
		rec.reqs = nil
		status, body := do(t, app, http.MethodGet, tc.path, "ua", "orga", "")
		if status != 200 {
			t.Fatalf("%s: status=%d, want 200; body=%s", tc.path, status, body)
		}
		r, ok := rec.find(tc.upstream)
		if !ok {
			t.Fatalf("%s: did not address %q; got %+v", tc.path, tc.upstream, rec.reqs)
		}
		if r.auth != "Bearer tok-A" {
			t.Fatalf("%s: used %q, want Bearer tok-A", tc.path, r.auth)
		}
		if s, _ := do(t, app, http.MethodGet, tc.path, "", "orga", ""); s != http.StatusForbidden {
			t.Fatalf("%s: unvalidated status=%d, want 403", tc.path, s)
		}
	}
}

// ── input hardening + account resolution ────────────────────────────────────────

// An explicit ?account= override must be a 32-hex id (defense against path
// injection), and it skips discovery.
func TestAccountOverrideValidated(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)

	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects?account=../../evil", "ua", "orga", ""); status != http.StatusBadRequest {
		t.Fatalf("hostile account override status=%d, want 400", status)
	}
	rec.reqs = nil
	override := "ffffffffffffffffffffffffffffffff"
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects?account="+override, "ua", "orga", ""); status != 200 {
		t.Fatalf("valid account override status=%d", status)
	}
	r, _ := rec.find("/pages/projects")
	if r.path != "/accounts/"+override+"/pages/projects" {
		t.Fatalf("override not honored: addressed %q", r.path)
	}
	if rec.hasExact("/accounts") {
		t.Fatal("discovery call made despite an explicit account override")
	}
}

// The account captured at connect time (ConnectionFor.ExternalID) is used without a
// live discovery round-trip.
func TestStoredAccountSkipsDiscovery(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	stored := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prev := connectionFor
	connectionFor = func(org, provider string) (integrations.Connection, bool) {
		if org == "orga" && provider == providerCloudflare {
			return integrations.Connection{ExternalID: stored}, true
		}
		return integrations.Connection{}, false
	}
	t.Cleanup(func() { connectionFor = prev })

	if s, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects", "ua", "orga", ""); s != 200 {
		t.Fatalf("status=%d", s)
	}
	r, ok := rec.find("/pages/projects")
	if !ok {
		t.Fatal("no pages request reached Cloudflare")
	}
	if r.path != "/accounts/"+stored+"/pages/projects" {
		t.Fatalf("addressed %q, want the stored-account path", r.path)
	}
	if rec.hasExact("/accounts") {
		t.Fatal("live discovery happened despite a stored account id")
	}
}
