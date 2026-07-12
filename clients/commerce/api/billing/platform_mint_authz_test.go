package billing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// mintRoutes are the money-mint / money-out billing routes that MUST be gated to
// the internal service token / platform SuperAdmin ONLY (C1). Table-driving them
// proves the gate is mounted on EVERY one — dropping it from any single route
// reopens the unlimited-self-credit hole.
var mintRoutes = []struct{ method, path, body string }{
	{http.MethodPost, "/v1/billing/deposit", `{"user":"acme/alice","amount":100}`},
	{http.MethodPost, "/v1/billing/refund", `{"user":"acme/alice","amount":100,"originalTransactionId":"x"}`},
	{http.MethodPost, "/v1/billing/credit-grants", `{"userId":"acme/alice","amountCents":100}`},
	{http.MethodPost, "/v1/billing/credit-grants/abc/void", `{}`},
	{http.MethodPost, "/v1/billing/grant-starter", `{"user":"acme/alice"}`},
	{http.MethodPost, "/v1/billing/customer-balance/adjustments", `{"customerId":"acme/alice","amount":100}`},
	{http.MethodPost, "/v1/billing/payouts", `{"amount":100}`},
	{http.MethodPost, "/v1/billing/payouts/abc/cancel", `{}`},
}

// engineWithSeed mounts the REAL billing Route() behind a pre-group middleware
// that sets context state exactly as the upstream chain would (EdgeAuth /
// IAMTokenRequired / the service-token branch).
func engineWithSeed(seed func(*gin.Context)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	if seed != nil {
		eng.Use(func(c *gin.Context) { seed(c); c.Next() })
	}
	Route(eng.Group("/v1"))
	return eng
}

// TestC1_OrgAdminDeniedOnEveryMintRoute is THE acceptance test: an org-admin JWT
// (org-level isAdmin=true → gateway-minted Admin|Live, isSuperAdmin=false) is
// FORBIDDEN (403) on every money-mint route. Before the fix, TokenRequired(Admin)
// admitted this principal and the handler minted a client-supplied amount → a live
// self-credit-unlimited-balance hole.
func TestC1_OrgAdminDeniedOnEveryMintRoute(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	orgAdmin := func(c *gin.Context) {
		c.Set("iam_authenticated", true)
		c.Set("permissions", bit.Field(permission.Admin|permission.Live))
		c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true}) // org owner, NOT global
	}
	eng := engineWithSeed(orgAdmin)
	for _, r := range mintRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, bytes.NewBufferString(r.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			eng.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status=%d body=%s, want 403 (org-admin must NOT reach a money-mint handler)",
					r.method, r.path, w.Code, w.Body.String())
			}
		})
	}
}

// TestC1_ServiceTokenMintsDeposit proves the legitimate money path STILL WORKS:
// the internal service token reaches Deposit and mints (201) end-to-end against a
// real datastore. cloud-api authenticates exactly this way (Bearer COMMERCE_SERVICE_TOKEN).
func TestC1_ServiceTokenMintsDeposit(t *testing.T) {
	const tok = "svc-secret-abc"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)
	ctx := ae.NewContext()
	defer ctx.Close()

	eng := engineWithSeed(func(c *gin.Context) { c.Set("context", ctx) })
	body := `{"user":"acmeorg/alice","amount":250}`
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/deposit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Org-Id", "acmeorg")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("service-token deposit: status=%d body=%s, want 201 (money path must keep working)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v (%s)", err, w.Body.String())
	}
	if tid, _ := resp["transactionId"].(string); tid == "" {
		t.Fatalf("deposit response missing transactionId: %v", resp)
	}
}

// TestC1_NonMintRouteNotOverBlocked is the control: the SAME org-admin identity is
// NOT 403 on a non-mint admin read (GET /balance). The fix narrows ONLY the
// money-mint routes; it must not break the rest of the admin billing surface.
func TestC1_NonMintRouteNotOverBlocked(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	ctx := ae.NewContext()
	defer ctx.Close()

	org := &organization.Organization{}
	org.Name = "acme"
	org.Live = true

	orgAdmin := func(c *gin.Context) {
		c.Set("context", ctx)
		c.Set("organization", org) // GetBalance calls middleware.GetOrganization
		c.Set("iam_authenticated", true)
		c.Set("permissions", bit.Field(permission.Admin|permission.Live))
		c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
	}
	eng := engineWithSeed(orgAdmin)

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/balance?user=acme/alice&currency=usd", nil)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("GET /balance for org-admin returned 403 — the mint gate over-blocked a non-mint route")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("GET /balance for org-admin: status=%d body=%s, want 200 (admitted, ungated read)", w.Code, w.Body.String())
	}
}
