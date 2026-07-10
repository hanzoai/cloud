// Package checkout — tenant handler tests against a real hanzo/base store.
//
// These tests construct a throwaway store under t.TempDir(), seed tenants
// via the repo, and exercise the handlers through the real gin router.
// They pin three security contracts that MUST NOT regress:
//
//  1. GET /v1/commerce/tenant never leaks provider credentials / client
//     secrets / KMS paths / BD endpoints. Public body is a tight
//     projection.
//  2. POST /_/commerce/tenants requires a PLATFORM (global) admin —
//     GlobalAdmin(): owner==admin or the explicit isGlobalAdmin claim. An org
//     owner (org-level isAdmin) or an org-mintable "superadmin" role gets 403.
//  3. GET /_/commerce/providers is tenant-scoped from the IAM `owner`
//     claim, never from the request. A caller whose owner has no tenant
//     row gets a byte-identical 404 to a caller whose owner exists but
//     is a different tenant (handled implicitly: the only way to see
//     someone else's data is to change your owner claim, which IAM will
//     not let you do).
package checkout

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/store"
)

// ─── helpers ────────────────────────────────────────────────────────────

// newHandlerStore constructs a store under t.TempDir and registers Cleanup.
func newHandlerStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.New(store.Config{DataDir: filepath.Join(dir, "commerce")})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(nil) })
	return s
}

// seedTenant creates a tenant with credentials-bearing fixtures so the
// public-view tests can assert redaction.
func seedTenant(t *testing.T, s *store.Store, name string, hosts ...string) *store.Tenant {
	t.Helper()
	tenant := &store.Tenant{
		Name:      name,
		Hostnames: hosts,
		Brand: store.BrandConfig{
			DisplayName:  strings.Title(name),
			LogoURL:      "https://cdn.example.test/" + name + ".png",
			PrimaryColor: "#0ea5e9",
		},
		IAM: store.IAMConfig{
			Issuer:   "https://id.example.test",
			ClientID: name + "-client",
		},
		IDV: store.IDVConfig{
			Provider: "persona",
			Endpoint: "https://withpersona.com/verify",
		},
		Providers: []store.Provider{
			{Name: "square", Enabled: true, KMSPath: "kms/commerce/" + name + "/square"},
			{Name: "braintree", Enabled: false, KMSPath: "kms/commerce/" + name + "/braintree"},
		},
		BDEndpoint:         "https://bd." + name + ".example.test",
		ReturnURLAllowlist: []string{"https://" + name + ".example.test"},
	}
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatalf("seedTenant %q: %v", name, err)
	}
	return tenant
}

// newRouterWithClaims builds a gin engine with the admin + public routes
// mounted, plus a pre-handler that injects the provided IAMClaims into the
// gin context. Passing nil claims simulates an unauthenticated caller.
func newRouterWithClaims(s *store.Store, claims *auth.IAMClaims) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Inject claims the same way iammiddleware does. The real middleware
	// sets additional keys (iam_org, iam_email, …) — the two helpers we
	// actually read are GetIAMClaims(c) and the presence of "iam_authenticated".
	router.Use(func(c *gin.Context) {
		if claims != nil {
			c.Set("iam_claims", claims)
			c.Set("iam_authenticated", true)
		}
		c.Next()
	})

	public := router.Group("/v1/commerce")
	MountPublicFromStore(public, s, nil)

	admin := router.Group("/_/commerce")
	MountTenantAdmin(admin, s)

	return router
}

// ─── public: GET /v1/commerce/tenant ────────────────────────────────────

func TestTenantJSONFromStore_RedactsSecrets(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "acme", "pay.acme.test")
	router := newRouterWithClaims(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/commerce/tenant", nil)
	req.Host = "pay.acme.test"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// MUST NOT leak: KMS paths, BD endpoint, disabled provider names,
	// client secrets (none are stored — confirm they never appear).
	forbidden := []string{
		"kms/commerce/acme/square",   // KMS path
		"kms/commerce/acme/braintree",
		"bd.acme.example.test",       // BD endpoint
		"braintree",                  // disabled provider
		"client_secret",              // never stored, confirm projection doesn't invent
	}
	for _, s := range forbidden {
		if strings.Contains(body, s) {
			t.Errorf("tenant JSON leaked %q — body:\n%s", s, body)
		}
	}

	// MUST be present: name, brand, public IAM config, enabled-only
	// providers, return-url allowlist.
	required := []string{
		"acme",
		"Acme", // brand display_name
		"#0ea5e9",
		"https://id.example.test",
		"acme-client",
		"square",
	}
	for _, s := range required {
		if !strings.Contains(body, s) {
			t.Errorf("tenant JSON missing %q — body:\n%s", s, body)
		}
	}
}

func TestTenantJSONFromStore_UnknownHostReturns404(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "acme", "pay.acme.test")
	router := newRouterWithClaims(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/commerce/tenant", nil)
	req.Host = "evil.example.test"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "evil.example.test") {
		t.Errorf("404 body echoed Host — body: %s", w.Body.String())
	}
	// Cache headers MUST NOT be public — unknown-host responses are
	// cacheable signals an attacker can use to probe.
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// ─── admin: POST /_/commerce/tenants ────────────────────────────────────

func TestCreateTenant_Unauthenticated_401(t *testing.T) {
	s := newHandlerStore(t)
	router := newRouterWithClaims(s, nil)

	body := []byte(`{"name":"new-tenant","hostnames":["pay.new.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTenant_TenantAdmin_403(t *testing.T) {
	s := newHandlerStore(t)
	// Tenant-admin role only — NOT isAdmin.
	claims := &auth.IAMClaims{
		Owner: "some-tenant",
		Roles: auth.FlexRoles{"admin", "owner"},
	}
	claims.Subject = "user-1"
	router := newRouterWithClaims(s, claims)

	body := []byte(`{"name":"new-tenant","hostnames":["pay.new.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	// MUST NOT have created the row — confirm via direct repo read.
	if _, err := s.Tenants.List(10, 0); err == nil {
		list, _ := s.Tenants.List(10, 0)
		if len(list) != 0 {
			t.Errorf("tenant-admin created a tenant despite 403: %+v", list)
		}
	}
}

func TestCreateTenant_Superadmin_201(t *testing.T) {
	s := newHandlerStore(t)
	// A real PLATFORM (global) admin: member of the admin org. Org-level
	// IsAdmin alone is NOT sufficient (see TestCreateTenant_OrgOwner_403).
	claims := &auth.IAMClaims{
		Owner:   "admin",
		IsAdmin: true,
	}
	claims.Subject = "superadmin-1"
	claims.Email = "z@hanzo.ai"
	router := newRouterWithClaims(s, claims)

	body := []byte(`{
		"name": "brand-new",
		"hostnames": ["pay.brand.test"],
		"brand": {"display_name": "Brand New"},
		"iam": {"issuer": "https://id.example.test", "client_id": "brand-new-client"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp createTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response JSON: %v; body=%s", err, w.Body.String())
	}
	if resp.ID == "" || resp.Name != "brand-new" {
		t.Errorf("response = %+v", resp)
	}
	// Confirm the row is actually in the store.
	got, err := s.Tenants.FindByID(resp.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Name != "brand-new" || len(got.Hostnames) != 1 {
		t.Errorf("stored tenant = %+v", got)
	}
}

func TestCreateTenant_DuplicateName_409(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "demo", "pay.demo.test")

	claims := &auth.IAMClaims{IsAdmin: true, Owner: "admin"}
	claims.Subject = "superadmin-1"
	router := newRouterWithClaims(s, claims)

	body := []byte(`{"name":"demo","hostnames":["pay.other.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTenant_InvalidHostname_400(t *testing.T) {
	s := newHandlerStore(t)
	claims := &auth.IAMClaims{IsAdmin: true, Owner: "admin"}
	claims.Subject = "superadmin-1"
	router := newRouterWithClaims(s, claims)

	// Whitespace-prefixed hostname — normalizeHostname rejects.
	body := []byte(`{"name":"bad","hostnames":[" pay.bad.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// ─── admin: GET /_/commerce/providers ───────────────────────────────────

func TestListProviders_Unauthenticated_401(t *testing.T) {
	s := newHandlerStore(t)
	router := newRouterWithClaims(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestListProviders_PlainUser_403(t *testing.T) {
	s := newHandlerStore(t)
	// Authenticated, but no admin / tenant-admin / superadmin role.
	claims := &auth.IAMClaims{Owner: "acme"}
	claims.Subject = "user-1"
	router := newRouterWithClaims(s, claims)

	req := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestListProviders_TenantAdmin_ScopedToOwner(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "acme", "pay.acme.test")
	seedTenant(t, s, "beta", "pay.beta.test")

	// Caller's owner is acme — response MUST be acme's providers. Org-level
	// admin is the robust tenant-admin signal (isAdmin claim, not a role name).
	claims := &auth.IAMClaims{Owner: "acme", IsAdmin: true}
	claims.Subject = "user-1"
	router := newRouterWithClaims(s, claims)

	req := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Tenant    string             `json:"tenant"`
		Providers []providerListItem `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if resp.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme (cross-tenant leak?)", resp.Tenant)
	}
	// MUST NOT leak KMS paths via the public view.
	if strings.Contains(w.Body.String(), "kms/") {
		t.Errorf("KMS path leaked in providers list: %s", w.Body.String())
	}
	// Both providers projected (enabled + disabled), but no credentials.
	if len(resp.Providers) != 2 {
		t.Errorf("providers count = %d, want 2; got %+v", len(resp.Providers), resp.Providers)
	}
}

// The cross-tenant probe: a caller whose IAM owner claim names a tenant
// that does NOT exist in the store MUST get a byte-identical 404 to a
// caller whose owner is empty — no existence oracle.
func TestListProviders_CrossTenantProbe_ByteIdentical404(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "acme", "pay.acme.test")

	// Case A: no-such-tenant owner with org-level admin.
	probeA := &auth.IAMClaims{Owner: "no-such-tenant", IsAdmin: true}
	probeA.Subject = "user-probe"
	routerA := newRouterWithClaims(s, probeA)
	reqA := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	wA := httptest.NewRecorder()
	routerA.ServeHTTP(wA, reqA)

	// Case B: empty-owner org-level admin.
	probeB := &auth.IAMClaims{Owner: "", IsAdmin: true}
	probeB.Subject = "user-empty"
	routerB := newRouterWithClaims(s, probeB)
	reqB := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	wB := httptest.NewRecorder()
	routerB.ServeHTTP(wB, reqB)

	if wA.Code != http.StatusNotFound {
		t.Errorf("probe A status = %d, want 404", wA.Code)
	}
	if wB.Code != http.StatusNotFound {
		t.Errorf("probe B status = %d, want 404", wB.Code)
	}
	if !bytes.Equal(wA.Body.Bytes(), wB.Body.Bytes()) {
		t.Errorf("cross-tenant probe body differs:\nA: %q\nB: %q", wA.Body.String(), wB.Body.String())
	}
	// Also: the tenant name MUST NOT appear in either body.
	if strings.Contains(wA.Body.String(), "acme") ||
		strings.Contains(wA.Body.String(), "no-such-tenant") {
		t.Errorf("probe A body leaks tenant name: %s", wA.Body.String())
	}
}
