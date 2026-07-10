// Copyright © 2026 Hanzo AI. MIT License.

package middleware

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
)

// RequireAdmin is the ONE admin gate the money-moving handlers use — IAM-aware
// AND legacy/service-token-aware. It fails closed (403) unless the caller is an
// admin, and is enforced INSIDE each money handler because the route-level
// TokenRequired(permission.Admin) middleware is a NO-OP on the IAM path: it
// short-circuits (c.Next) for any IAM-authenticated request WITHOUT checking the
// Admin bit (Red HIGH-4). A handler must never trust that gate on its own.
//
// Precedence (fail-closed):
//  1. Permissions bit — the legacy access token AND the service token both set
//     c["permissions"] with permission.Admin when the caller is admin
//     (middleware/accesstoken.go). Honored FIRST so the trusted M2M service-token
//     money path (cloud-api → commerce, which carries X-Org-Id) is authorized by
//     its verified token, not mistaken for a spoofable IAM-edge header identity.
//  2. IAM identity — the gateway/EdgeAuth-minted, JWT-verified claims must carry
//     org-level IsAdmin OR platform GlobalAdmin.
//
// These are per-ORG money actions (the caller acts within its own resolved
// namespace), so org-level admin suffices and a global admin is also allowed
// (superset). Cross-tenant/platform-global actions gate on the STRICTER
// GlobalAdmin predicate instead (api/catalog.requireGlobalAdmin,
// checkout.isSuperadmin), never this one.
//
// Returns true when admin; writes a 403 and returns false otherwise. Reads
// c["permissions"] without MustGet so a handler mounted without the token gate
// fails closed (403) rather than panicking (500).
func RequireAdmin(c *gin.Context) bool {
	if v, ok := c.Get("permissions"); ok {
		if f, ok := v.(bit.Field); ok && f.Has(permission.Admin) {
			return true
		}
	}
	if iammiddleware.IsIAMAuthenticated(c) {
		claims := iammiddleware.GetIAMClaims(c) // non-nil by contract
		if claims.IsAdmin || claims.GlobalAdmin() {
			return true
		}
	}
	http.Fail(c, 403, "admin privileges required", errors.New("caller is not an admin"))
	return false
}
