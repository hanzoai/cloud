// Copyright © 2026 Hanzo AI. MIT License.

package transaction

import (
	"bytes"
	"context"
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

// invokeCreate drives the transaction Create handler with a real org + datastore
// context and a caller identity, then returns the recorder. It reproduces the
// production reach of POST /v1/transaction (adminRequired route middleware +
// handler); the identity closure sets whichever principal the case models.
func invokeCreate(org *organization.Organization, ctx context.Context, identity func(*gin.Context), body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Set("context", ctx)
	identity(c)
	req := httptest.NewRequest(http.MethodPost, "/v1/transaction", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	Create(c)
	return w
}

// orgAdmin is the C1 adversary: an org OWNER (org-level isAdmin, permissions
// Admin|Live) who is NOT a platform global admin and NOT the internal service
// token. This is exactly the principal that must be able to move its own funds
// (withdraw/transfer) but NOT mint spendable balance.
func orgAdmin(c *gin.Context) {
	c.Set("permissions", bit.Field(permission.Admin|permission.Live))
	c.Set("iam_authenticated", true)
	c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
}

// mintPrincipal is the internal service token (cloud-api → commerce): carries the
// Admin permission AND the verified service-token marker → MayMintMoney true.
func mintPrincipal(c *gin.Context) {
	c.Set("permissions", bit.Field(permission.Admin|permission.Live))
	c.Set("service_token_verified", true)
}

func moneyOrg(name string) *organization.Organization {
	o := &organization.Organization{}
	o.Name = name
	o.Live = true
	return o
}

// TestCreate_OrgAdminDepositToIAMUser_Denied is C1-b: the exact PoC — an org
// admin POSTing a $1,000,000 deposit into the gateway-spendable IAM-user wallet
// must be REFUSED (403), NOT minted. Before the fix this returned 201.
func TestCreate_OrgAdminDepositToIAMUser_Denied(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"type":"deposit","destinationId":"acme/attacker","destinationKind":"iam-user","amount":100000000,"currency":"usd"}`
	w := invokeCreate(org, ctx, orgAdmin, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("org-admin $1M iam-user deposit: status=%d body=%s, want 403 (no self-mint)", w.Code, w.Body.String())
	}
}

// TestCreate_OrgAdminIAMUserTransfer_Denied — the forged-Transfer variant into
// the gateway wallet is likewise refused for an org admin.
func TestCreate_OrgAdminIAMUserTransfer_Denied(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"type":"transfer","sourceId":"x","sourceKind":"x","destinationId":"acme/attacker","destinationKind":"iam-user","amount":5000,"currency":"usd"}`
	w := invokeCreate(org, ctx, orgAdmin, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("org-admin iam-user transfer: status=%d body=%s, want 403", w.Code, w.Body.String())
	}
}

// TestCreate_MintPrincipalDeposit_Allowed — control: the internal service token
// (the legitimate cloud-api money path) still mints (201). The gate narrows WHO
// may mint; it does not break the authorized path.
func TestCreate_MintPrincipalDeposit_Allowed(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"type":"deposit","destinationId":"acme/user","destinationKind":"iam-user","amount":5000,"currency":"usd"}`
	w := invokeCreate(org, ctx, mintPrincipal, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("service-token iam-user deposit: status=%d body=%s, want 201 (authorized mint path intact)", w.Code, w.Body.String())
	}
}

// TestCreate_OrgAdminWithdraw_Allowed — control: a Withdraw (moving the org's OWN
// funds out) is NOT a mint, so an org admin is still allowed (the gate is precise,
// not a blanket lock on the endpoint).
func TestCreate_OrgAdminWithdraw_Allowed(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	// Seed a balance to withdraw from (ungated internal write — allowed).
	seed := `{"type":"deposit","destinationId":"acme/spender","destinationKind":"iam-user","amount":5000,"currency":"usd"}`
	if w := invokeCreate(org, ctx, mintPrincipal, seed); w.Code != http.StatusCreated {
		t.Fatalf("seed deposit: status=%d body=%s, want 201", w.Code, w.Body.String())
	}

	body := `{"type":"withdraw","sourceId":"acme/spender","sourceKind":"iam-user","amount":1000,"currency":"usd"}`
	w := invokeCreate(org, ctx, orgAdmin, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("org-admin withdraw of own funds: status=%d body=%s, want 201 (not a mint)", w.Code, w.Body.String())
	}
}
