package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

// TestDeployRoutesRequireAdmin (RED LOW-2): every mounted /v1/deploy route EXCEPT
// the public health probe must 403 without X-User-IsAdmin=true, and must NOT 403
// with it. This guards the guard: a future refactor that adds an /v1/deploy or
// /v1/deploy/ui route without wrapping it in guard() breaks this test.
func TestDeployRoutesRequireAdmin(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, fakeSvc()) // the COMPLETE surface: native + engine + dashboard

	guarded := []struct{ method, path string }{
		// engine
		{"POST", "/v1/deploy/reconcile"},
		// projection API — clean /v1/deploy/<resource> (no /api/, no inner /v1)
		{"GET", "/v1/deploy/settings"},
		{"GET", "/v1/deploy/version"},
		{"GET", "/v1/deploy/account/can-i/applications/get/x"},
		{"GET", "/v1/deploy/applications"},
		{"GET", "/v1/deploy/applications/cloud"},
		{"GET", "/v1/deploy/applications/cloud/resource-tree"},
		{"POST", "/v1/deploy/applications/cloud/sync"},
		{"POST", "/v1/deploy/applications/cloud/rollback"},
		// destination clusters + AppProjects (the applications view's side lists)
		{"GET", "/v1/deploy/clusters"},
		{"GET", "/v1/deploy/projects"},
	}
	for _, r := range guarded {
		// WITHOUT admin → 403 (the guard, fail-closed).
		resp, err := app.Fiber().Test(httptest.NewRequest(r.method, r.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", r.method, r.path, err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusForbidden {
			t.Errorf("%s %s WITHOUT admin = %d, want 403 (route is not guarded!)", r.method, r.path, code)
		}
		// WITH admin → the guard passes (handler may 200/404/503, never the guard's 403).
		req := httptest.NewRequest(r.method, r.path, nil)
		req.Header.Set("X-User-IsAdmin", "true")
		resp2, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s admin: %v", r.method, r.path, err)
		}
		code2 := resp2.StatusCode
		_ = resp2.Body.Close()
		if code2 == http.StatusForbidden {
			t.Errorf("%s %s WITH admin = 403 (guard must pass for a validated SuperAdmin)", r.method, r.path)
		}
	}

	// The health probe is DELIBERATELY public (liveness without a JWT) — never 403.
	resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/deploy/health", nil))
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if resp.StatusCode == http.StatusForbidden {
		t.Error("/v1/deploy/health must stay public (probe-able without a JWT)")
	}
	_ = resp.Body.Close()

	// Sign-in is DELIBERATELY public — these routes ARE how a browser becomes a
	// SuperAdmin, so gating them behind SuperAdmin would be circular. They grant
	// nothing: /login only redirects, /callback refuses anything but a token this
	// deployment verifies for a member of the admin org.
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/deploy/login"},
		{"GET", "/v1/deploy/callback"},
		{"POST", "/v1/deploy/logout"},
	} {
		resp, err := app.Fiber().Test(httptest.NewRequest(r.method, r.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", r.method, r.path, err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code == http.StatusForbidden {
			t.Errorf("%s %s = 403; sign-in cannot require the role it grants", r.method, r.path)
		}
	}

	// Logout must NOT be reachable as a GET: it changes state, and a cross-site
	// top-level navigation carries a SameSite=Lax cookie.
	resp, err = app.Fiber().Test(httptest.NewRequest("GET", "/v1/deploy/logout", nil))
	if err != nil {
		t.Fatalf("GET logout: %v", err)
	}
	code := resp.StatusCode
	_ = resp.Body.Close()
	if code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Errorf("GET /v1/deploy/logout = %d, want 404/405 (logout is POST-only)", code)
	}
}

// TestUserInfoIsPublicBootstrap: the SPA's "am I signed in?" question must be
// answerable WITHOUT being signed in — it is an XHR client, so the document bounce
// never fires for it and a 403 here is a dead end with no route to sign-in. The
// anonymous answer carries the sign-in URL and NOTHING that identifies anyone.
func TestUserInfoIsPublicBootstrap(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, fakeSvc())

	resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/deploy/session/userinfo", nil))
	if err != nil {
		t.Fatalf("userinfo: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous userinfo = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()

	if body["loggedIn"] != false {
		t.Errorf("anonymous loggedIn = %v, want false", body["loggedIn"])
	}
	if body["loginUrl"] != "/v1/deploy/login" {
		t.Errorf("loginUrl = %v, want /v1/deploy/login", body["loginUrl"])
	}
	// No identity may leak to an anonymous caller — not even an empty placeholder.
	for _, k := range []string{"username", "iss", "groups", "email", "org", "logoutUrl"} {
		if _, present := body[k]; present {
			t.Errorf("anonymous userinfo leaked %q = %v", k, body[k])
		}
	}
	// A cookie must never be minted by a read.
	for _, ck := range resp.Cookies() {
		if ck.Value != "" {
			t.Errorf("userinfo minted a cookie %s=%q", ck.Name, ck.Value)
		}
	}

	// A SuperAdmin gets the real identity on the same route.
	req := httptest.NewRequest("GET", "/v1/deploy/session/userinfo", nil)
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-User-Id", "cto")
	resp2, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("admin userinfo: %v", err)
	}
	var admin map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&admin); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp2.Body.Close()
	if admin["loggedIn"] != true || admin["username"] != "cto" {
		t.Errorf("admin userinfo = %v, want loggedIn:true username:cto", admin)
	}
}
