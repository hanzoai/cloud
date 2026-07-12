package affiliate

// payout_gate_test.go proves the [MEDIUM] C1 miss is closed: the OSS-contributor
// treasury payout machinery (executePayouts and friends) is service-token /
// platform-SuperAdmin ONLY — an org-level admin can no longer drive treasury
// disbursement (CreditGrants / Stripe transfers / on-chain HUSD).

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// mountAffiliate mounts the real affiliate Route() behind a seed and returns the
// engine plus the concrete path registered for the given handler short-name.
func mountAffiliate(t *testing.T, seed func(*gin.Context), handler string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	if seed != nil {
		eng.Use(func(c *gin.Context) { seed(c); c.Next() })
	}
	Route(eng.Group("/v1"))

	for _, ri := range eng.Routes() {
		if strings.HasSuffix(ri.Handler, "."+handler) {
			return eng, ri.Path
		}
	}
	t.Fatalf("route for handler %q not found", handler)
	return nil, ""
}

// TestPayoutExecute_OrgAdminDenied is the acceptance test: an org admin POSTing
// the payout executor is 403 — it can no longer disburse the treasury.
func TestPayoutExecute_OrgAdminDenied(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	eng, path := mountAffiliate(t, func(c *gin.Context) {
		c.Set("iam_authenticated", true)
		c.Set("permissions", bit.Field(permission.Admin|permission.Live))
		c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
	}, "executePayouts")

	req := httptest.NewRequest(http.MethodPost, path+"?execute=true", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("org-admin %s: status=%d body=%s, want 403", path, w.Code, w.Body.String())
	}
}

// TestPayoutCalculate_OrgAdminDenied proves the sibling payout-family write is
// gated too (RED grouped calculate/sbom with execute).
func TestPayoutCalculate_OrgAdminDenied(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	eng, path := mountAffiliate(t, func(c *gin.Context) {
		c.Set("iam_authenticated", true)
		c.Set("permissions", bit.Field(permission.Admin|permission.Live))
		c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
	}, "calculatePayouts")

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"totalRevenueCents":100000}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("org-admin %s: status=%d body=%s, want 403", path, w.Code, w.Body.String())
	}
}

// TestPayoutExecute_ServiceTokenReaches proves the legitimate CronJob / cloud-api
// path is preserved: the internal service token passes the gate (NOT 403). The
// dry-run executor runs behind it.
func TestPayoutExecute_ServiceTokenReaches(t *testing.T) {
	const tok = "svc-payout"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)
	ctx := ae.NewContext()
	defer ctx.Close()

	eng, path := mountAffiliate(t, func(c *gin.Context) { c.Set("context", ctx) }, "executePayouts")

	// dry-run (no ?execute=true) — the gate must ADMIT the service token.
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Org-Id", "payoutorg")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("service-token %s was 403 — the gate wrongly denied the internal service path", path)
	}
}
