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
	method, path, auth, ctype string
	body                      []byte
}

func (c *capture) add(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.reqs = append(c.reqs, capturedReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
	c.mu.Unlock()
}

// find returns the first captured request whose path contains sub.
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

// do drives one request through the mounted app with the given minted identity
// headers (as SanitizeIdentity would have set them) and returns status + body.
func do(t *testing.T, app *zip.App, method, path, user, org, body string) (int, string) {
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
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ── tenant isolation (the crown jewel) ──────────────────────────────────────────

// A request with no validated principal (no X-User-Id) is refused 403 and NEVER
// reaches token custody or Cloudflare — the forged-X-Org-Id path is dead.
func TestForgedOrgWithoutPrincipalIs403(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"victim": "tok-victim"}, rec, nil)

	// No X-User-Id, but a client-supplied X-Org-Id naming another org.
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

	// orgA drives Pages list; assert the /pages request carried orgA's token.
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
	// orgB drives the SAME route; assert it carried orgB's token, never orgA's.
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
// proving the handler derives the org only from the validated principal.
func TestBodyOrgFieldCannotRedirectToken(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A", "orgb": "tok-B"}, rec, nil)

	// orgA creates a project with a hostile body carrying org/account fields for orgB.
	body := `{"name":"site","org":"orgb","organizationId":"orgb","account":"ffffffffffffffffffffffffffffffff"}`
	if status, resp := do(t, app, http.MethodPost, "/v1/cloudflare/pages/projects", "ua", "orga", body); status != 200 {
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

// ── wired behavior ──────────────────────────────────────────────────────────────

// Pages list relays the Cloudflare result verbatim and addresses the resolved
// account path.
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

// Worker script PUT sends the modern multipart module upload (metadata + module
// parts) with the caller-org token.
func TestWorkersScriptPutMultipart(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	body := `{"script":"export default { fetch(){ return new Response('hi') } }","mainModule":"worker.js"}`
	status, resp := do(t, app, http.MethodPut, "/v1/cloudflare/workers/scripts/hello", "ua", "orga", body)
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

// Zone routes are zone-scoped and do NOT resolve an account.
func TestWorkersRouteBindZoneScoped(t *testing.T) {
	rec := &capture{}
	zone := "abcdef0123456789abcdef0123456789"
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	body := `{"pattern":"example.com/*","script":"hello"}`
	status, resp := do(t, app, http.MethodPost, "/v1/cloudflare/workers/zones/"+zone+"/routes", "ua", "orga", body)
	if status != 200 {
		t.Fatalf("status=%d resp=%s", status, resp)
	}
	if r, ok := rec.find("/zones/" + zone + "/workers/routes"); !ok {
		t.Fatalf("route bind did not address the zone path; got %+v", rec.reqs)
	} else if r.auth != "Bearer tok-A" {
		t.Fatalf("route bind used %q, want Bearer tok-A", r.auth)
	}
	// zone-scoped: no account discovery call was made.
	if _, ok := rec.find("/accounts"); ok {
		t.Fatal("route bind resolved an account; zone routes must not")
	}
}

// ── stubs never lie ─────────────────────────────────────────────────────────────

// R2/KV/D1 stub routes answer an honest 501 for a CONNECTED org — never a fake 200
// — and still enforce the validated-principal gate.
func TestStubRoutesReturn501NeverSuccess(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, path := range []string{
		"/v1/cloudflare/r2/buckets",
		"/v1/cloudflare/kv/namespaces",
		"/v1/cloudflare/d1/databases",
	} {
		status, body := do(t, app, http.MethodGet, path, "ua", "orga", "")
		if status != http.StatusNotImplemented {
			t.Fatalf("%s: status=%d, want 501; body=%s", path, status, body)
		}
		if strings.Contains(strings.ToLower(body), `"success":true`) || strings.Contains(strings.ToLower(body), `"ok":true`) {
			t.Fatalf("%s: stub returned a misleading success: %s", path, body)
		}
		// gate: unvalidated caller is refused even on a stub route.
		if s, _ := do(t, app, http.MethodGet, path, "", "orga", ""); s != http.StatusForbidden {
			t.Fatalf("%s: unvalidated status=%d, want 403", path, s)
		}
	}
}

// ── input hardening ─────────────────────────────────────────────────────────────

// An explicit ?account= override must be a 32-hex id (defense against path
// injection into /accounts/{id}/...).
func TestAccountOverrideValidated(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)

	// A hostile override is rejected 400 before any CF call.
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects?account=../../evil", "ua", "orga", ""); status != http.StatusBadRequest {
		t.Fatalf("hostile account override status=%d, want 400", status)
	}
	// A valid override is honored (no discovery call needed).
	rec.reqs = nil
	override := "ffffffffffffffffffffffffffffffff"
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/pages/projects?account="+override, "ua", "orga", ""); status != 200 {
		t.Fatalf("valid account override status=%d", status)
	}
	r, _ := rec.find("/pages/projects")
	if r.path != "/accounts/"+override+"/pages/projects" {
		t.Fatalf("override not honored: addressed %q", r.path)
	}
	if _, ok := rec.find("/accounts?"); ok {
		t.Fatal("discovery call made despite an explicit account override")
	}
}
