// Copyright © 2026 Hanzo AI. MIT License.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware/svcorg"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
)

// runGate drives TokenRequired(masks...) through a real gin engine with the
// given pre-set context state (iam_authenticated / permissions), returning the
// HTTP status and whether the protected handler was reached. COMMERCE_SERVICE_TOKEN
// is cleared so only the IAM branch under test is exercised.
func runGate(t *testing.T, masks []bit.Mask, seed func(*gin.Context)) (int, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")

	reached := false
	eng := gin.New()
	eng.Use(func(c *gin.Context) { seed(c); c.Next() })
	eng.POST("/x", TokenRequired(masks...), func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	return w.Code, reached
}

func iamAuth(perms bit.Field) func(*gin.Context) {
	return func(c *gin.Context) {
		c.Set("iam_authenticated", true)
		c.Set("permissions", perms)
	}
}

// TestTokenRequired_IAMBranchEnforcesMasks is the core fix-2 proof: the IAM
// branch used to bare-c.Next() for ANY authenticated principal, which made every
// masked gate (TokenRequired(Admin, Published) on the checkout money routes) a
// NO-OP — a low-privilege or forged (perms=0) IAM caller reached the money
// handlers. It now enforces the requested masks against the gateway-minted
// permissions (bit.Field.Has = intersection, so Admin|Published is satisfied by
// EITHER bit). A no-mask gate (billing) still admits any authenticated principal.
func TestTokenRequired_IAMBranchEnforcesMasks(t *testing.T) {
	published := []bit.Mask{permission.Admin, permission.Published}

	cases := []struct {
		name       string
		masks      []bit.Mask
		perms      bit.Field
		wantStatus int
		wantReach  bool
	}{
		{"admin mints (Admin|Live intersects Admin)", published,
			bit.Field(permission.Admin | permission.Live), http.StatusOK, true},
		{"published storefront principal mints", published,
			bit.Field(permission.Published | permission.Live), http.StatusOK, true},
		{"forged/low-priv perms=0 denied", published,
			bit.Field(0), http.StatusForbidden, false},
		{"read-only scope denied a mint", published,
			bit.Field(permission.ReadStore), http.StatusForbidden, false},
		{"no-mask gate admits any authed principal (billing)", nil,
			bit.Field(0), http.StatusOK, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reached := runGate(t, tc.masks, iamAuth(tc.perms))
			if status != tc.wantStatus || reached != tc.wantReach {
				t.Fatalf("status=%d reached=%v, want status=%d reached=%v",
					status, reached, tc.wantStatus, tc.wantReach)
			}
		})
	}
}

// TestTokenRequired_BareOrgHeaderIsNotAuthenticated proves the IsIAMAuthenticated
// hardening end-to-end: a request carrying ONLY a client X-Org-Id header (no
// validated iam_authenticated, no valid token, no service token) is NOT treated
// as an IAM principal — it falls through to token auth and 401s. Before the fix,
// the bare header alone satisfied IsIAMAuthenticated and reached the handler.
func TestTokenRequired_BareOrgHeaderIsNotAuthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")

	reached := false
	eng := gin.New()
	eng.POST("/x", TokenRequired(permission.Admin, permission.Published), func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Org-Id", "hanzo") // spoofed org selector, no token behind it
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized || reached {
		t.Fatalf("bare X-Org-Id must not authenticate: status=%d reached=%v, want 401,false", w.Code, reached)
	}
}

// TestTokenRequired_ServiceTokenResolveFailsFast is the money-critical regression
// for the 2026-07-04 wedge: when the bearer IS the verified service token but the
// backing store cannot resolve the org, the branch must fail CLOSED and RETRYABLE
// (503) — NOT fall through to the legacy per-org-token path (which Peeks the
// 64-hex service token as a JWT → "Invalid Segments" → a misleading 401 that made
// cloud-api treat a transient DB hiccup as a bad credential and 402 customers).
// A nil default datastore forces svcorg.Resolve to error deterministically.
func TestTokenRequired_ServiceTokenResolveFailsFast(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const svc = "svc-token-abc123"
	t.Setenv("COMMERCE_SERVICE_TOKEN", svc)

	// No default DB installed → GetOrCreate inside svcorg.Resolve errors, so we
	// exercise the resolve-failure branch. Use a distinct org slug so this test's
	// failure is never masked by another test's cached success.
	datastore.SetDefaultDB(nil)
	svcorg.Invalidate("failorg")

	reached := false
	eng := gin.New()
	eng.POST("/x", TokenRequired(), func(c *gin.Context) { // no-mask billing-style gate
		reached = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+svc)
	req.Header.Set("X-Org-Id", "failorg")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if reached {
		t.Fatalf("handler must NOT be reached when service-token org resolve fails")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("verified-service-token resolve failure must be 503 (retryable), got %d; "+
			"a 401 means it fell through to the legacy Peek path (the wedge)", w.Code)
	}
}
