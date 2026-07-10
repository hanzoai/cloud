// Package commerce — integration test proving P8-H1 is fixed: the store-
// backed /v1/commerce/tenant (public) and /_/commerce/tenants (admin) routes
// are wired into the production commerce binary, not just attached inside a
// per-test router.
//
// We boot the real commerce App via NewWithConfig → Bootstrap. Bootstrap
// initializes the hanzo/base-backed CommerceStore, IAM middleware (disabled
// in this test via IAM.Enabled=false), gin router, and calls setupRoutes. No
// mocks, no test-only wiring — this is the shape that ships.
package commerce

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// bootTestCommerce constructs a real App with the lightest viable config and
// runs Bootstrap. Infra services that fail to connect degrade — KMS disabled,
// NATS absent, Stripe seed gated on STRIPE_SECRET_KEY (unset), IAM disabled.
// The only thing that MUST work is the commerce/store bootstrap + route wire.
func bootTestCommerce(t *testing.T) *App {
	t.Helper()

	dir := t.TempDir()
	cfg := &Config{
		DataDir:      filepath.Join(dir, "data"),
		Dev:          false,
		Secret:       "test-secret",
		HTTPAddr:     "127.0.0.1:0",
		QueryTimeout: 30e9,
	}
	cfg.IAM.Enabled = false // no JWKS fetch; admin routes still register, caller JWT absent → 401
	cfg.KMS.Enabled = false

	// Ensure we do not touch the environment's STRIPE / SQL vars.
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("COMMERCE_STRIPE_SEED", "false")
	t.Setenv("SQL_URL", "")
	t.Setenv("COMMERCE_DATA_DIR", cfg.DataDir)
	t.Setenv("COMMERCE_BASE_URL", "")

	app := NewWithConfig(cfg)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Shutdown()
	})
	return app
}

// probe issues a request directly against the booted router, bypassing the
// HTTP server listen step but exercising the exact same gin.Engine.
func probe(t *testing.T, app *App, method, path string, host string, body []byte) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if host != "" {
		req.Host = host
	}
	w := httptest.NewRecorder()
	app.Router.ServeHTTP(w, req)
	return w.Result()
}

// TestRoutes_WiredInProduction proves P8-H1 is fixed: the store-backed
// /v1/commerce/tenant and /_/commerce/tenants endpoints are wired in the
// booted commerce binary's gin router. If either returns the NoRoute 404
// body `{"error":"not found"}` (MountSPA's isAPIPath branch) the wiring is
// missing.
//
// We do NOT assert specific success semantics — authentication is disabled
// and the tenant collection is empty — only that the router recognizes the
// paths and dispatches to a registered handler rather than NoRoute.
func TestRoutes_WiredInProduction(t *testing.T) {
	app := bootTestCommerce(t)

	cases := []struct {
		name   string
		method string
		path   string
		host   string
		body   []byte
	}{
		{name: "public_tenant_lookup", method: http.MethodGet, path: "/v1/commerce/tenant", host: "pay.example.test"},
		{name: "admin_create_tenant", method: http.MethodPost, path: "/_/commerce/tenants", body: []byte(`{"name":"probe"}`)},
		{name: "admin_list_providers", method: http.MethodGet, path: "/_/commerce/providers"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := probe(t, app, tc.method, tc.path, tc.host, tc.body)
			defer resp.Body.Close()
			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)

			// NoRoute SPA fallback for API paths returns exactly
			// `{"error":"not found"}`. If we see that, the endpoint is NOT
			// wired. Any other body proves the route reached a handler.
			if body == `{"error":"not found"}` {
				t.Fatalf("%s %s reached NoRoute SPA fallback — route not wired. status=%d body=%s",
					tc.method, tc.path, resp.StatusCode, body)
			}

			// Sanity: must NOT be 405 Method Not Allowed.
			if resp.StatusCode == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s returned 405 — route wiring method mismatch", tc.method, tc.path)
			}
		})
	}
}

// TestRoutes_PublicTenantResolvesOrg proves GET /v1/commerce/tenant resolves
// the tenant from the IAM org (host→brand→org), returning 200 with an
// org-as-tenant JSON — NOT a 404 against a separate tenant registry. Even with
// no org row and no Square env in the test binary, resolution degrades to the
// deployment default brand + env, so the pay SPA always receives a usable
// tenant carrying `name` (the org slug) and a `square` block. This is the fix
// for the pay.hanzo.ai "unknown tenant" 404 + dead card form.
func TestRoutes_PublicTenantResolvesOrg(t *testing.T) {
	app := bootTestCommerce(t)

	resp := probe(t, app, http.MethodGet, "/v1/commerce/tenant", "pay.p8.test", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (org-as-tenant). body=%s", resp.StatusCode, string(bodyBytes))
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("tenant JSON decode: %v", err)
	}
	if name, _ := body["name"].(string); name == "" {
		t.Fatalf("tenant JSON missing name (org slug): %v", body)
	}
	if _, ok := body["square"]; !ok {
		t.Fatalf("tenant JSON missing square block (card form needs it): %v", body)
	}
}

// TestRoutes_AdminTenantCreatePathExists verifies the POST admin endpoint
// exists. With IAM disabled, the handler's own claim check 401s — which is
// a wired-handler signal, not a NoRoute miss.
func TestRoutes_AdminTenantCreatePathExists(t *testing.T) {
	app := bootTestCommerce(t)

	resp := probe(t, app, http.MethodPost, "/_/commerce/tenants", "", []byte(`{"name":"x"}`))
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	if body == `{"error":"not found"}` {
		t.Fatalf("POST /_/commerce/tenants not wired — status=%d body=%s", resp.StatusCode, body)
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Logf("POST /_/commerce/tenants status=%d body=%s (accepted — handler ran)", resp.StatusCode, body)
	}
}
