// Package checkout — authorization regression tests for the tenant-admin
// handlers (Red: org-admin vs SuperAdmin conflation + role-name conflation).
//
// These pin the corrected model end-to-end through the real gin router and
// store, reusing the harness in tenant_handler_test.go (newHandlerStore,
// seedTenant, newRouterWithClaims):
//
//   - CreateTenant (cross-tenant) requires a PLATFORM SuperAdmin. Org-level
//     isAdmin (an org owner) and org-mintable role NAMES do NOT qualify.
//   - tenant-admin (org-scoped) is the robust isAdmin claim, not a role name.
package checkout

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/auth"
)

// postCreateTenant runs POST /_/commerce/tenants with the given claims against
// a fresh store and returns the status + body.
func postCreateTenant(t *testing.T, claims *auth.IAMClaims) (int, string) {
	t.Helper()
	s := newHandlerStore(t)
	router := newRouterWithClaims(s, claims)
	body := []byte(`{"name":"authz-tenant","hostnames":["pay.authz.test"]}`)
	req := httptest.NewRequest(http.MethodPost, "/_/commerce/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestCreateTenant_OrgOwner_403 is the core Red HIGH regression: an org owner
// carries org-level IsAdmin=true within their OWN org, but that MUST NOT grant
// platform superadmin (cross-tenant create). Under the old code (isSuperadmin
// trusted IsAdmin) this returned 201 — the escalation.
func TestCreateTenant_OrgOwner_403(t *testing.T) {
	claims := &auth.IAMClaims{Owner: "maxpower", IsAdmin: true}
	claims.Subject = "dave"
	code, body := postCreateTenant(t, claims)
	if code != http.StatusForbidden {
		t.Fatalf("org owner (isAdmin, owner=maxpower) status=%d want 403; body=%s", code, body)
	}
}

// TestCreateTenant_ForgedSuperadminRole_403 is the role-name conflation
// regression: an org-mintable role NAME ("superadmin"/"platform-admin") MUST
// NOT grant platform superadmin. Under the old code (isSuperadmin matched the
// role string) this returned 201 — escalation by self-assigned role.
func TestCreateTenant_ForgedSuperadminRole_403(t *testing.T) {
	claims := &auth.IAMClaims{Owner: "evil-org", Roles: auth.FlexRoles{"superadmin", "platform-admin"}}
	claims.Subject = "mallory"
	code, body := postCreateTenant(t, claims)
	if code != http.StatusForbidden {
		t.Fatalf("forged superadmin role status=%d want 403; body=%s", code, body)
	}
}

// TestCreateTenant_SuperAdminByFlag_201 proves the explicit isSuperAdmin
// claim grants superadmin even from a non-admin org — forwards-compat with the
// explicit gateway X-User-IsSuperAdmin signal.
func TestCreateTenant_SuperAdminByFlag_201(t *testing.T) {
	claims := &auth.IAMClaims{Owner: "hanzo", IsSuperAdmin: true}
	claims.Subject = "platform-op"
	code, body := postCreateTenant(t, claims)
	if code != http.StatusCreated {
		t.Fatalf("SuperAdmin-by-flag status=%d want 201; body=%s", code, body)
	}
}

// TestListProviders_ForgedAdminRole_403 proves tenant-admin (org-scoped) is the
// robust isAdmin claim, not a fragile role NAME: a user carrying only a role
// string "admin" (isAdmin=false) is no longer treated as a tenant admin.
func TestListProviders_ForgedAdminRole_403(t *testing.T) {
	s := newHandlerStore(t)
	seedTenant(t, s, "acme", "pay.acme.test")
	claims := &auth.IAMClaims{Owner: "acme", Roles: auth.FlexRoles{"admin"}} // role only, no isAdmin claim
	claims.Subject = "role-user"
	router := newRouterWithClaims(s, claims)

	req := httptest.NewRequest(http.MethodGet, "/_/commerce/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("role-name-only tenant-admin status=%d want 403; body=%s", w.Code, w.Body.String())
	}
}
