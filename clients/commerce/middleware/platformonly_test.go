// Copyright © 2026 Hanzo AI. MIT License.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// runMintGate drives the EXACT production money-mint middleware chain —
// TokenRequired(permission.Admin) THEN PlatformOnly() — that api/billing.Route
// mounts on /deposit, /refund, /credit-grants, /customer-balance/adjustments and
// /grant-starter, to a sentinel handler. Returns the HTTP status and whether the
// sentinel was reached. seed pre-sets context state exactly as the real upstream
// middleware would (EdgeAuth/IAMTokenRequired mint iam_authenticated + permissions
// + iam_claims); the service-token branch is exercised via a real Bearer + env.
func runMintGate(t *testing.T, seed func(*gin.Context), reqSetup func(*http.Request)) (int, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	reached := false
	eng := gin.New()
	if seed != nil {
		eng.Use(func(c *gin.Context) { seed(c); c.Next() })
	}
	eng.POST("/x", TokenRequired(permission.Admin), PlatformOnly(), func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if reqSetup != nil {
		reqSetup(req)
	}
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	return w.Code, reached
}

func iamIdentity(perms bit.Field, claims *auth.IAMClaims) func(*gin.Context) {
	return func(c *gin.Context) {
		c.Set("iam_authenticated", true)
		c.Set("permissions", perms)
		c.Set("iam_claims", claims)
	}
}

// TestPlatformOnly_OrgAdminDeniedMint is THE C1 proof at the gate: an ORG-level
// admin (org owner — org-level IsAdmin=true, so the gateway mints Admin|Live, but
// isSuperAdmin=false) PASSES TokenRequired(Admin) yet is DENIED (403) by
// PlatformOnly on the money-mint chain; the handler is never reached. This is the
// exact principal (e.g. maxpower) that could previously self-mint unlimited balance.
func TestPlatformOnly_OrgAdminDeniedMint(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	orgAdmin := &auth.IAMClaims{Owner: "maxpower", IsAdmin: true} // org owner, NOT global
	status, reached := runMintGate(t, iamIdentity(bit.Field(permission.Admin|permission.Live), orgAdmin), nil)
	if status != http.StatusForbidden || reached {
		t.Fatalf("org-admin on money-mint chain: status=%d reached=%v, want 403 & not-reached (this was the C1 hole)", status, reached)
	}
}

// TestPlatformOnly_SuperAdminMints proves a PLATFORM SuperAdmin — the explicit
// isSuperAdmin claim OR membership in the "admin" org — passes the same chain
// (the legitimate human-superadmin path RED's spec requires to still work).
func TestPlatformOnly_SuperAdminMints(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	for _, gc := range []*auth.IAMClaims{
		{Owner: "hanzo", IsSuperAdmin: true},
		{Owner: "admin"}, // the SuperAdmin org
	} {
		status, reached := runMintGate(t, iamIdentity(bit.Field(permission.Admin|permission.Live), gc), nil)
		if status != http.StatusOK || !reached {
			t.Fatalf("SuperAdmin %+v: status=%d reached=%v, want 200 & reached", gc, status, reached)
		}
	}
}

// TestPlatformOnly_ServiceTokenMints proves the internal service token (cloud-api →
// commerce, COMMERCE_SERVICE_TOKEN) passes the chain — the legitimate money path
// that MUST keep working. Drives the REAL service-token branch of TokenRequired
// (which stamps the marker PlatformOnly reads).
func TestPlatformOnly_ServiceTokenMints(t *testing.T) {
	const tok = "svc-secret-xyz"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)
	ctx := ae.NewContext()
	defer ctx.Close()

	// The service-token branch resolves the caller-named org via GetContext(c).
	seed := func(c *gin.Context) { c.Set("context", ctx) }
	status, reached := runMintGate(t, seed, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Org-Id", "svc-org")
	})
	if status != http.StatusOK || !reached {
		t.Fatalf("service token: status=%d reached=%v, want 200 & reached (money path must keep working)", status, reached)
	}
}

// TestPlatformOnly_AdminBitAloneDenied pins the gate's core invariant in isolation:
// holding the org-level Admin bit — or being merely IAM-authenticated as an org
// owner — is NOT sufficient; only the service-token marker or SuperAdmin passes.
// This is the anti-conflation that closes C1: a legacy per-org access token and an
// org owner, both of which carry Admin, are refused. Fail-closed on empty context.
func TestPlatformOnly_AdminBitAloneDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		seed func(*gin.Context)
	}{
		{"nothing set (fail-closed)", func(c *gin.Context) {}},
		{"org-level Admin permission bit only (legacy access token)", func(c *gin.Context) {
			c.Set("permissions", bit.Field(permission.Admin|permission.Live))
		}},
		{"iam org-admin, not global", func(c *gin.Context) {
			c.Set("iam_authenticated", true)
			c.Set("permissions", bit.Field(permission.Admin|permission.Live))
			c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			eng := gin.New()
			eng.Use(func(c *gin.Context) { tc.seed(c); c.Next() })
			eng.POST("/x", PlatformOnly(), func(c *gin.Context) { reached = true; c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			w := httptest.NewRecorder()
			eng.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden || reached {
				t.Fatalf("%s: status=%d reached=%v, want 403 & not-reached", tc.name, w.Code, reached)
			}
		})
	}
}

// TestIsServiceToken_FailClosed proves the marker predicate never reads a
// non-service request as a service caller: absent or non-bool value → false.
func TestIsServiceToken_FailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if IsServiceToken(c) {
		t.Fatal("empty context: IsServiceToken must be false (fail-closed)")
	}
	c.Set(ctxKeyServiceToken, "true") // wrong type, not bool
	if IsServiceToken(c) {
		t.Fatal("non-bool marker: IsServiceToken must be false (fail-closed)")
	}
	c.Set(ctxKeyServiceToken, true)
	if !IsServiceToken(c) {
		t.Fatal("bool true marker: IsServiceToken must be true")
	}
}
