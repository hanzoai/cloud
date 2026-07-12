// Copyright © 2026 Hanzo AI. MIT License.

package middleware

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// AuthorizeMint stamps this request's datastore context as authorized to mint
// spendable balance, so a downstream ledger write (through an
// Organization.Namespaced-derived datastore) passes mintauth.Enforce. It is the
// ONE way an HTTP handler grants the mint capability the ledger sink demands —
// call it AFTER establishing a proven mint authority (MayMintMoney, a settled
// payment, or a server-fixed grant). It re-stores the gin "context" key, which
// Organization.Namespaced reads, so any datastore the handler builds afterward
// is authorized. Writes that build their datastore BEFORE the authority is known
// (top-up after the charge, webhook after signature) instead authorize the
// specific write via mintauth.WithAuthorized on that write's context.
func AuthorizeMint(c *gin.Context) {
	c.Set("context", mintauth.WithAuthorized(GetContext(c)))
}

// PlatformOnly restricts a route to the ONLY two principals allowed to MINT
// money / spendable balance:
//
//  1. the internal service (cloud-api → commerce), authenticated by a bearer
//     equal to COMMERCE_SERVICE_TOKEN — recorded by TokenRequired's service-token
//     branch as IsServiceToken(c); and
//  2. a Hanzo PLATFORM SuperAdmin — auth.IAMClaims.SuperAdmin(): the
//     spoof-proof isSuperAdmin claim (gateway/EdgeAuth X-User-IsSuperAdmin) OR
//     membership in the "admin" org.
//
// It deliberately does NOT admit the org-level Admin bit (permission.Admin). An
// org OWNER carries org-level IsAdmin=true within their own org (IAM), which the
// gateway/EdgeAuth mints into X-User-Permissions = Admin|Live; a legacy per-org
// access token can hold Admin too. Gating the money-mint billing routes on that
// bit — via TokenRequired(permission.Admin) alone — let ANY org owner self-credit
// unlimited balance (POST /v1/billing/deposit &c.) → unlimited free inference.
// That is the real-money-GA blocker this gate closes. It is the same
// org-admin-vs-SuperAdmin anti-conflation the codebase enforces for cross-org
// actions (checkout tenant admin, the edge billing ?org override).
//
// MOUNT IT AFTER TokenRequired(permission.Admin): TokenRequired resolves the org
// (service-token + legacy paths), sets c["permissions"], and stamps the
// service-token marker; PlatformOnly then NARROWS who may proceed to the handler.
// It never widens access — a caller already rejected by TokenRequired (401) never
// reaches here.
//
// Fail-closed: neither signal present → 403, handler not reached.
func PlatformOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if MayMintMoney(c) {
			// Proven mint principal → authorize the ledger sink for this request,
			// so the PlatformOnly-gated mint handlers (deposit, refund,
			// credit-grants, payouts, cycle, auto-recharge, …) all mint without
			// per-handler changes while org-admins are still 403'd above.
			AuthorizeMint(c)
			c.Next()
			return
		}
		http.Fail(c, 403,
			"This operation requires platform-administrator or internal-service credentials.",
			errors.New("money-mint route: caller is neither the internal service token nor a platform SuperAdmin"))
	}
}

// MayMintMoney is THE single predicate for "may this caller MINT money /
// spendable balance". It admits exactly the two principals PlatformOnly admits:
//
//  1. the verified internal service token (cloud-api → commerce), IsServiceToken(c); and
//  2. a Hanzo PLATFORM SuperAdmin, auth.IAMClaims.SuperAdmin() — the
//     spoof-proof isSuperAdmin claim OR membership in the "admin" org.
//
// It deliberately does NOT admit the org-level Admin bit (an org OWNER's IAM
// isAdmin, or a legacy per-org access token). Use it wherever a mint decision is
// made OUTSIDE the route-middleware chain — the ZAP-over-HTTP dispatcher gates
// its money-mint method (billing.deposit) with it, and the allotment grant clamps
// a client plan override on it — so there is ONE expression of the mint principal,
// shared by the route gate (PlatformOnly) and every in-handler gate. Fail-closed:
// neither signal present → false.
func MayMintMoney(c *gin.Context) bool {
	return IsServiceToken(c) || iammiddleware.GetIAMClaims(c).SuperAdmin()
}
