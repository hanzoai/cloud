package deploy

import (
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
		// native
		{"GET", "/v1/deploy/applications"},
		{"GET", "/v1/deploy/cloud/tree"},
		{"GET", "/v1/deploy/cloud/resource/x"},
		{"GET", "/v1/deploy/cloud/logs"},
		{"POST", "/v1/deploy/cloud/rollback"},
		{"POST", "/v1/deploy/cloud/sync"},
		// engine
		{"POST", "/v1/deploy/reconcile"},
		// dashboard bootstrap + projection + actions + static
		{"GET", "/v1/deploy/ui/api/v1/settings"},
		{"GET", "/v1/deploy/ui/api/v1/session/userinfo"},
		{"GET", "/v1/deploy/ui/api/version"},
		{"GET", "/v1/deploy/ui/api/v1/account/can-i/applications/get/x"},
		{"GET", "/v1/deploy/ui/api/v1/applications"},
		{"GET", "/v1/deploy/ui/api/v1/applications/cloud"},
		{"GET", "/v1/deploy/ui/api/v1/applications/cloud/resource-tree"},
		{"POST", "/v1/deploy/ui/api/v1/applications/cloud/sync"},
		{"POST", "/v1/deploy/ui/api/v1/applications/cloud/rollback"},
		{"GET", "/v1/deploy/ui"},
		{"GET", "/v1/deploy/ui/index.html"},
		{"GET", "/v1/deploy/ui/applications/cloud"}, // an SPA client-side route
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
}
