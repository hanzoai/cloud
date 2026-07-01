package cloud

// SanitizeIdentity — the in-binary identity trust boundary.
//
// THE PROBLEM. zip.Ctx.Org()/IsAdmin()/User() read the X-Org-Id / X-User-IsAdmin
// / X-User-Id request headers verbatim. In production the gateway
// (hanzoai/gateway) is the sole minter of those headers: it strips any
// client-supplied copy and re-injects them from a validated IAM JWT (HIP-0026).
// cloud TRUSTS that contract. But cloud-api is also reachable WITHOUT the gateway
// in front — directly in-cluster (cloud-api.hanzo.svc:8000, used by console2's
// BFF) and historically on the public host cloud-api.hanzo.ai. On those paths a
// caller can simply send `X-User-IsAdmin: true` and every cloud admin gate
// (c.IsAdmin()) believes it. That is the forgeable-admin trust boundary.
//
// THE FIX. This middleware runs FIRST — before BillingGate and every subsystem —
// and rewrites the request's identity headers so each downstream c.IsAdmin() /
// c.Org() / c.User() reflects a VALIDATED principal, never a raw client header.
// It is ONE place; every existing IsAdmin()/Org() reader (pricingsvc admin
// catalog + /v1/pricing/sync, provisioningsvc, mlsvc, evalsvc, plansvc) becomes
// trustworthy without touching a single handler.
//
// ADMIN IS GLOBAL-ADMIN. The gateway mints X-User-IsAdmin from the JWT `isAdmin`
// bool, which IAM also sets true for ORG admins (an org owner). The cloud admin
// surfaces (global catalog writes, the literal "admin" tenant bucket) mean
// GLOBAL admin. So the admin authority here is granted ONLY to a validated
// principal whose org IS the admin org (owner == adminOrg — IAM's IsGlobalAdmin),
// matching the gateway's admin-guard. An org admin gets NO admin authority.

import (
	"strings"

	"github.com/hanzoai/zip"
)

// cookieTokenNames are the session-cookie names that may carry an IAM access
// token (mirrors iamauth.CookieToken).
var cookieTokenNames = []string{"iam_access_token", "access_token", "hanzo_token"}

// authorityHeaders are the identity/authority headers the gateway mints and the
// ONLY ones a downstream may trust. The sanitizer deletes every one on ingress
// so nothing a client sent survives as identity, then re-injects from a
// validated principal.
//
// Deliberately NOT in this list: org/project SUB-SCOPES (X-Project-Id,
// X-Environment). Those are sub-scopes WITHIN an org that legit callers pass
// through verbatim (evalsvc reads the canonical X-Project-Id; provisioning
// reads project/env) — they are not identity authority. (Whether a service
// should trust a client X-Project-Id for tenant scope, versus minting it from a
// project-membership check, is a separate Phase-2 question — out of scope for
// the admin boundary this fix closes.)
var authorityHeaders = []string{
	"X-User-Id",
	"X-Org-Id",
	"X-Roles",
	"X-User-Permissions",
	"X-User-Email",
	"X-Phone-Number",
	"X-User-IsAdmin",
	// Legacy identity aliases an attacker might try; none are org sub-scopes.
	"X-User-Role",
	"X-User-Roles",
	"X-User-Name",
	"X-Tenant-Id",
	"X-Tenant-ID",
	"X-Org",
}

// SanitizeIdentity returns the identity-trust-boundary middleware.
//
// Per request:
//   - ALWAYS delete every header in authorityHeaders (a client copy never
//     survives — this alone kills X-User-IsAdmin forgery).
//   - Validate a Bearer / Basic / session-cookie JWT, if present:
//     global admin (claims.isAdmin && owner == adminOrg)
//     → X-User-IsAdmin=true; X-Org-Id = the requested org when present
//     (admin org-switch), else owner.
//     any other principal (incl. org admins, normal users)
//     → X-Org-Id pinned to owner; NO admin. A client org cannot widen
//     scope.
//   - No / invalid / opaque-API-key credential:
//     → NO admin (forgery dead), and the client's X-Org-Id is restored for
//     the Phase-1 data path (residual below).
//
// PHASE-1 RESIDUAL (documented; not a regression vs. today): with no validatable
// bearer, the client X-Org-Id is passed through for DATA scoping. cloud's data
// plane has no session of its own yet ("Auth stays gateway-owned in Phase 1")
// and the console browser data path depends on this header. So a direct-to-pod
// caller can still SELECT a tenant for DATA reads. Closing that needs the data
// path to carry a bearer universally OR the NetworkPolicy locked to gateway-only
// (which would break console2's legitimate direct in-cluster BFF) — Phase-2. The
// ADMIN boundary (this fix's P0) is closed on every path regardless, because
// X-User-IsAdmin is NEVER restored from client input.
//
// FAIL MODE. If the validator can't verify a token (JWKS unreachable on a cold
// cache, issuer/audience misconfigured), the request resolves anonymous: admin
// fails SECURE (legit admins get 403 until config is corrected — a bounded
// availability cost), while org scoping is unaffected (the gateway-minted
// X-Org-Id is restored on the no-principal path). Never fails OPEN to admin.
func SanitizeIdentity(v *identityValidator, adminOrg string) zip.Handler {
	adminOrg = strings.TrimSpace(adminOrg)
	return func(c *zip.Ctx) error {
		req := c.Fiber().Request()

		// Capture the requested org before stripping (admin org-switch input +
		// Phase-1 data passthrough), then delete every authority header.
		cliOrg := strings.TrimSpace(string(req.Header.Peek("X-Org-Id")))
		for _, h := range authorityHeaders {
			req.Header.Del(h)
		}

		if claims := validatedPrincipal(c, v); claims != nil {
			owner := strings.TrimSpace(claims.Owner)
			if id := claims.userID(); id != "" {
				req.Header.Set("X-User-Id", id)
			}
			if claims.Email != "" {
				req.Header.Set("X-User-Email", claims.Email)
			}
			switch {
			case claims.IsAdmin && owner != "" && owner == adminOrg:
				// Verified GLOBAL admin: admin authority + honored org-switch.
				req.Header.Set("X-User-IsAdmin", "true")
				if cliOrg != "" {
					req.Header.Set("X-Org-Id", cliOrg)
				} else {
					req.Header.Set("X-Org-Id", owner)
				}
			case owner != "":
				// Any other principal: pinned to their own org, never admin.
				req.Header.Set("X-Org-Id", owner)
			}
			return c.Continue()
		}

		// No verified principal: admin authority is gone for good. Restore the
		// client org for the Phase-1 data path only (see residual note above).
		if cliOrg != "" {
			req.Header.Set("X-Org-Id", cliOrg)
		}
		return c.Continue()
	}
}

// validatedPrincipal extracts a token (Bearer, Basic, then session cookie) and
// validates it. Returns nil when the credential is absent, opaque (an hk-/sk-
// API key — not a JWT), or invalid — so a bad credential yields anonymity, never
// trust. A nil validator (unconfigured) also yields nil: the sanitizer still
// strips authority headers, so forgery stays dead even with no validator.
func validatedPrincipal(c *zip.Ctx, v *identityValidator) *idClaims {
	if v == nil {
		return nil
	}
	tok := bearerFromAuth(c.Header("Authorization"))
	if tok == "" {
		tok = bearerFromAuth(c.Header("X-Authorization"))
	}
	if tok == "" {
		tok = basicFromAuth(c.Header("Authorization"))
	}
	if tok == "" {
		for _, name := range cookieTokenNames {
			if val := c.Fiber().Cookies(name); val != "" {
				tok = val
				break
			}
		}
	}
	if tok == "" || isAPIKey(tok) {
		return nil
	}
	claims, err := v.validate(tok)
	if err != nil {
		return nil
	}
	return claims
}
