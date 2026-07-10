package store

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
)

// mintStorefrontToken mints (idempotently) the org's storefront read key — an
// access token carrying ONLY permission.Published, the least-privilege key a
// logged-out shopper's storefront uses to read the org's published catalog
// (design path b: "a per-org storefront Published key, minted once, kept in
// KMS"). It is org-bound (signed by the org's own SecretKey, subject = org id),
// so unlike the shared service token it can NEVER act on another tenant and
// carries no write/admin scope.
//
// Admin-gated (the route wraps middleware.TokenRequired(permission.Admin)): only
// an org admin — or the platform service token scoped to this org via X-Org-Id —
// may mint its own org's storefront key. Re-minting rotates the key (RemoveToken
// drops the prior "storefront" token, invalidating it).
//
// Returns { status, org, token }. The caller stores `token` in KMS and injects
// it as HANZO_COMMERCE_STOREFRONT_TOKEN on the storefront deployment.
func mintStorefrontToken(c *gin.Context) {
	// Authoritative admin gate. The route's TokenRequired(Admin) NO-OPS on the
	// IAM path (EdgeAuth/gateway-minted identity), so enforce admin here the same
	// way the money handlers do — IAM-aware, fail-closed — or a non-admin org
	// member could mint their org's storefront key.
	if !middleware.RequireAdmin(c) {
		return
	}

	org, ok := middleware.GetOrganizationOK(c)
	if !ok || org == nil {
		http.Fail(c, 400, "organization required", errors.New("no organization in context"))
		return
	}

	org.RemoveToken("storefront")
	token := org.AddToken("storefront", permission.Published)
	if err := org.Put(); err != nil {
		http.Fail(c, 500, "failed to persist storefront token", err)
		return
	}

	http.Render(c, 200, gin.H{"status": "ok", "org": org.Name, "token": token})
}
