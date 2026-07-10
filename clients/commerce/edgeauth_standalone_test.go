// Copyright © 2026 Hanzo AI. MIT License.
//
// Package commerce — standalone-router security regression for the
// in-cluster identity-header forge (Red CRITICAL).
//
// Red proved that against the directly-exposed commerce-api a pod could
// `POST commerce.hanzo.svc:8001/_/commerce/tenants` with
// `X-Org-Id: admin` + `X-User-IsGlobalAdmin: true` and get 201 — platform
// superadmin by header forgery. Root cause: the identity boundary (EdgeAuth
// strip+remint) was mounted AFTER setupRoutes had already registered
// /_/commerce/* and /v1/commerce/*, and gin applies engine.Use() only to
// routes registered AFTER the Use() call, so those groups were unguarded.
//
// These tests boot the REAL App via NewWithConfig → Bootstrap with
// COMMERCE_EDGE_AUTH=true — the production shape — and assert the boundary
// now wraps every route: forged identity headers are stripped before any
// handler, so the forge yields 401 with no tenant row, while the
// service-token money path (no X-Org-Id, require=false) still flows through.
package commerce

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// bootEdgeCommerce boots a real App with the standalone edge trust boundary
// ENABLED (COMMERCE_EDGE_AUTH=true) and IAM disabled (no JWKS fetch; EdgeAuth
// therefore strips forged identity but mints nothing — exactly the state an
// unauthenticated forge attempt hits). Mirrors bootTestCommerce's degraded
// infra config so the only thing under test is the boundary + route wiring.
func bootEdgeCommerce(t *testing.T) *App {
	t.Helper()

	// EdgeAuth() reads COMMERCE_EDGE_AUTH at construction time (during
	// Bootstrap → installIdentityBoundary), so it MUST be set before boot.
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	// require-identity OFF: the binary-edge gate is incompatible with the
	// service-token money path (no X-Org-Id). This is the deployed default.
	t.Setenv("COMMERCED_REQUIRE_IDENTITY", "false")
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("COMMERCE_STRIPE_SEED", "false")
	t.Setenv("SQL_URL", "")

	dir := t.TempDir()
	cfg := &Config{
		DataDir:      filepath.Join(dir, "data"),
		Secret:       "test-secret",
		HTTPAddr:     "127.0.0.1:0",
		QueryTimeout: 30e9,
	}
	cfg.IAM.Enabled = false
	cfg.KMS.Enabled = false
	t.Setenv("COMMERCE_DATA_DIR", cfg.DataDir)
	t.Setenv("COMMERCE_BASE_URL", "")

	app := NewWithConfig(cfg)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

// TestStandaloneForge_TenantCreate_Blocked is the CRITICAL regression. A
// forged platform-superadmin POST to /_/commerce/tenants MUST be stripped at
// the boundary and rejected (401), and MUST NOT create a tenant row. On the
// pre-fix code (EdgeAuth mounted after setupRoutes) this returned 201.
func TestStandaloneForge_TenantCreate_Blocked(t *testing.T) {
	app := bootEdgeCommerce(t)

	const forgedName = "red-probe-forged-superadmin"
	body := []byte(`{"name":"` + forgedName + `","hostnames":["pay.forged.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// The exact forge Red used, plus the org-level admin header for good measure.
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-IsGlobalAdmin", "true")
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-User-Id", "mallory")

	w := httptest.NewRecorder()
	app.Router.ServeHTTP(w, req)

	if w.Code == http.StatusCreated {
		t.Fatalf("FORGE SUCCEEDED: POST /_/commerce/tenants with forged identity returned 201 — boundary not wired. body=%s", w.Body.String())
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged tenant-create status=%d, want 401 (identity stripped → unauthenticated); body=%s", w.Code, w.Body.String())
	}

	// Defense-in-depth: confirm no tenant row leaked into the store.
	tenants, err := app.CommerceStore.Tenants.List(500, 0)
	if err != nil {
		t.Fatalf("Tenants.List: %v", err)
	}
	for _, tn := range tenants {
		if tn.Name == forgedName {
			t.Fatalf("forged tenant %q was created despite %d response — store mutated", forgedName, w.Code)
		}
	}
}

// TestStandaloneForge_ListProviders_Blocked proves the same boundary covers
// the sibling admin endpoint: a forged tenant-admin GET is stripped → 401,
// never 200 (which would leak provider config).
func TestStandaloneForge_ListProviders_Blocked(t *testing.T) {
	app := bootEdgeCommerce(t)

	req := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	req.Header.Set("X-Org-Id", "victim-tenant")
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-Roles", "admin,owner,superadmin")

	w := httptest.NewRecorder()
	app.Router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("forged provider-list returned 200 — boundary not wired; body=%s", w.Body.String())
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged provider-list status=%d, want 401; body=%s", w.Code, w.Body.String())
	}
}

// TestStandaloneMoneyPath_NotBlockedByBoundary proves the boundary does not
// regress the service-token money path. require=false means a service-token-
// shaped request is NOT 401'd by the boundary — it reaches the per-route handler.
//
// The property under test is "not blocked by the boundary" (no boundary 401),
// asserted by the handler actually running. GET /v1/commerce/tenant is wired to
// the RESOLVER-backed checkout.TenantJSON(orgResolver) (see commerce.go), NOT the
// store-backed TenantJSONFromStore. By deliberate, documented design
// (checkout/org_resolver.go: "Resolution never 404s for a well-formed host"),
// proven by checkout.TestBrandForHost + TestRoutes_PublicTenantResolvesOrg, the
// pay-SPA boot endpoint resolves EVERY well-formed host to a usable tenant (a
// recognized brand, else the deployment default) so "add credits" always
// renders; it 404s ONLY a malformed Host. So a reached handler returns 200 with
// a tenant JSON. The earlier 404 / {"error":"unknown tenant"} expectation here
// assumed the store-backed handler, which is not what this route wires — that
// was a wrong expectation, corrected (not weakened: the boundary-not-401 check
// is preserved and the handler-ran signal is now the exact 200 tenant JSON).
func TestStandaloneMoneyPath_NotBlockedByBoundary(t *testing.T) {
	app := bootEdgeCommerce(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/commerce/tenant", nil)
	req.Host = "pay.unknown.test"
	// Service-token shape: opaque Bearer + X-Org-Id, no JWT.
	req.Header.Set("Authorization", "Bearer st_opaque_service_token")
	req.Header.Set("X-Org-Id", "maxpower")

	w := httptest.NewRecorder()
	app.Router.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("boundary 401'd a service-token-shaped request — money path regression; body=%s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/commerce/tenant status=%d, want 200 (handler reached; resolver returns a usable tenant for a well-formed host); body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("tenant JSON decode: %v; body=%s", err, w.Body.String())
	}
	if name, _ := body["name"].(string); name == "" {
		t.Fatalf("tenant JSON missing name (org slug) — the resolver-backed handler must have run: %v", body)
	}
}
