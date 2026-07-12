// Copyright © 2026 Hanzo AI. MIT License.

package billing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// invokeSub drives a subscription handler with an org + datastore context + a
// caller identity (org-admin vs mint principal), reproducing the production reach.
func invokeSub(org *organization.Organization, ctx context.Context, identity func(*gin.Context), h gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Set("context", ctx)
	identity(c)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscriptions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h(c)
	return w
}

// c1OrgAdmin models the adversary: an org owner (org-level isAdmin) who is NOT a
// platform SuperAdmin and NOT the internal service token → MayMintMoney false.
func c1OrgAdmin(c *gin.Context) {
	c.Set("permissions", bit.Field(permission.Admin|permission.Live))
	c.Set("iam_authenticated", true)
	c.Set("iam_claims", &auth.IAMClaims{Owner: "acme", IsAdmin: true})
}

// c1MintPrincipal models cloud-api's internal service token → MayMintMoney true.
func c1MintPrincipal(c *gin.Context) {
	c.Set("permissions", bit.Field(permission.Admin|permission.Live))
	c.Set("service_token_verified", true)
}

// TestCreateSubscription_OrgAdminPaidTier_Denied is C1-a: an org admin must NOT
// self-create a PAID-tier ("max": $200/mo, $100/mo included allotment) Active
// subscription with zero payment. Before the fix this returned 201, and the
// forged Active sub then unlocked the "max" allotment mint.
func TestCreateSubscription_OrgAdminPaidTier_Denied(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"userId":"acme/self","planId":"max"}`
	w := invokeSub(org, ctx, c1OrgAdmin, CreateBillingSubscription, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("org-admin self-subscribe to paid 'max': status=%d body=%s, want 403 (no free paid tier)", w.Code, w.Body.String())
	}
}

// TestCreateSubscription_OrgAdminFreeTier_Allowed — control: a FREE tier
// ("developer": $0, no included allotment) stays self-serve for an org admin.
func TestCreateSubscription_OrgAdminFreeTier_Allowed(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"userId":"acme/self","planId":"developer"}`
	w := invokeSub(org, ctx, c1OrgAdmin, CreateBillingSubscription, body)
	if w.Code == http.StatusForbidden {
		t.Fatalf("org-admin self-subscribe to FREE 'developer': status=403 body=%s, want allowed (free tier is self-serve)", w.Body.String())
	}
}

// TestCreateSubscription_MintPrincipalPaidTier_Allowed — control: cloud-api (the
// internal service token) may still create a paid-tier subscription (e.g. after a
// real payment). The gate narrows WHO, not the legitimate path.
func TestCreateSubscription_MintPrincipalPaidTier_Allowed(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")

	body := `{"userId":"acme/self","planId":"max"}`
	w := invokeSub(org, ctx, c1MintPrincipal, CreateBillingSubscription, body)
	if w.Code == http.StatusForbidden {
		t.Fatalf("service-token create paid 'max': status=403 body=%s, want allowed (authorized mint principal)", w.Body.String())
	}
}

// seedSub writes a subscription for user with the given plan slug, status, provider
// type, and (optionally) a linked invoice id.
func seedSub(t *testing.T, db *datastore.Datastore, user, slug string, status subscription.Status, providerType, invoiceID string) {
	t.Helper()
	s := subscription.New(db)
	s.UserId = user
	s.Status = status
	s.ProviderType = providerType
	s.CurrentInvoiceId = invoiceID
	s.Plan.Slug = slug
	s.PlanId = slug
	if err := s.Create(); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
}

// TestSubscriptionPlanSlug_PaymentBackedOnly is C1-a's entitlement clamp: a PAID
// tier's plan is returned as the mintable entitlement ONLY when the subscription
// is payment-backed. A self-created internal Active 'max' sub yields "" (so the
// allotment clamps to $0); a Stripe-backed or invoiced one yields "max".
func TestSubscriptionPlanSlug_PaymentBackedOnly(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")
	db := datastore.New(org.Namespaced(ctx))

	// Forged: internal, Active, paid tier, NO invoice → NOT entitlement-bearing.
	seedSub(t, db, "acme/forger", "max", subscription.Active, "internal", "")
	if got := subscriptionPlanSlug(db, "acme/forger"); got != "" {
		t.Fatalf("forged internal Active 'max' sub: subscriptionPlanSlug=%q, want \"\" (not payment-backed → no minted allotment)", got)
	}

	// Real: Stripe-managed Active paid sub → entitlement-bearing.
	seedSub(t, db, "acme/payer", "max", subscription.Active, "stripe", "")
	if got := subscriptionPlanSlug(db, "acme/payer"); got != "max" {
		t.Fatalf("stripe-backed 'max' sub: subscriptionPlanSlug=%q, want \"max\"", got)
	}

	// Real: internal but INVOICED (billing cycle collected) → entitlement-bearing.
	seedSub(t, db, "acme/invoiced", "max", subscription.Active, "internal", "inv_123")
	if got := subscriptionPlanSlug(db, "acme/invoiced"); got != "max" {
		t.Fatalf("invoiced internal 'max' sub: subscriptionPlanSlug=%q, want \"max\"", got)
	}

	// A FREE tier needs no backing (mints nothing) → returned as-is.
	seedSub(t, db, "acme/freeuser", "developer", subscription.Active, "internal", "")
	if got := subscriptionPlanSlug(db, "acme/freeuser"); got != "developer" {
		t.Fatalf("free 'developer' sub: subscriptionPlanSlug=%q, want \"developer\"", got)
	}
}

// TestPlanForGrant_OrgAdminCannotAnchorOnForgedSub proves the clamp end-to-end:
// an org admin (not a mint principal) with a forged internal Active 'max' sub
// cannot have the grant resolve to 'max', even naming it explicitly.
func TestPlanForGrant_OrgAdminCannotAnchorOnForgedSub(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("acme")
	db := datastore.New(org.Namespaced(ctx))
	seedSub(t, db, "acme/forger", "max", subscription.Active, "internal", "")

	// Build an org-admin gin context (MayMintMoney false).
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c1OrgAdmin(c)

	if got := planForGrant(c, db, "acme/forger", "max"); got != "" {
		t.Fatalf("org-admin planForGrant with forged 'max' sub + explicit 'max': got %q, want \"\" (clamped — allotment mints $0)", got)
	}
}
