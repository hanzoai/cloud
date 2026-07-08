package cloud

// SanitizeIdentity — the in-binary identity trust boundary.
//
// THE PROBLEM. zip.Ctx.Org()/IsAdmin()/User() read the X-Org-Id / X-User-IsAdmin
// / X-User-Id request headers verbatim. In production the gateway
// (hanzoai/gateway) is the sole minter of those headers: it strips any
// client-supplied copy and re-injects them from a validated IAM JWT (HIP-0026).
// cloud TRUSTS that contract. But cloud-api is also reachable WITHOUT the gateway
// in front — directly in-cluster (cloud-api.hanzo.svc:8000, used by console's
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
	"context"
	"strings"
	"unicode"

	"github.com/hanzoai/beego/v2/server/web"
	"github.com/zap-proto/zip"
)

// OrgHasUnsafeRune reports whether s carries any whitespace, control, or
// zero-width/format rune — the class that defeats the injectivity of the
// org→tenant map. strings.TrimSpace (and fasthttp's own header-value OWS
// trimming) silently drop such runes at the edges, so two DISTINCT IAM org
// names ("acme" vs "acme ", or an NBSP/ZWSP variant) would collapse onto ONE
// tenant-<slug> namespace / image ref — a cross-tenant fold. The identity trust
// boundary REFUSES to grant tenancy from an org bearing one of these (fail
// secure) instead of folding it, so distinct raw names never collide and no
// namespace is ever derived from an invisible-character identifier.
//
// Case / '-' / '.' / other visible punctuation are deliberately NOT unsafe:
// those fold INJECTIVELY through the org-slug hash (provisioning.SanitizeOrg).
// Only the invisible / edge-trimmable class — which no injective fold can
// survive once transport strips it — is rejected here. A legitimate IAM org
// slug never contains such a rune, so no real caller is affected.
func OrgHasUnsafeRune(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// cookieTokenNames are the session-cookie names that may carry an IAM access
// token (mirrors iamauth.CookieToken).
var cookieTokenNames = []string{"iam_access_token", "access_token", "hanzo_token"}

// authorityHeaders are the identity/authority headers the gateway mints and the
// ONLY ones a downstream may trust. The sanitizer deletes every one on ingress
// so nothing a client sent survives as identity, then re-injects from a
// validated principal.
//
// Deliberately NOT in this list: org SUB-SCOPES (X-Project-Id, X-App-Id,
// X-Environment). Those are sub-scopes WITHIN an org, NOT identity authority, so
// they are not minted from the token like the headers above. They ARE still
// sanitized — but as sub-scopes, in a separate pass (sanitizeSubScopes, keyed on
// subScopeHeaders): every raw client copy is deleted on ingress, then X-Project-Id
// is RE-INJECTED for a validated principal only when it is not a cross-org claim
// (projectIsForeign — tenant_scope.go refuses a project REGISTERED to a DIFFERENT
// org), and both are dropped on the anonymous path. This IS the "project-
// membership check UNDER the validated org" the data plane always required: a
// service must never derive tenant scope from a raw X-Project-Id, and after this
// pass a surviving X-Project-Id is either the caller's OWN registered project or
// an unregistered free-form label that can only ever scope the caller's own org
// (every consumer AND-s it with the validated org). The native evals subsystem,
// which scopes exclusively by c.Org() and ignores X-Project-Id, is unaffected.
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

// subScopeHeaders are org SUB-SCOPES (project/app) — caller-provided narrowings,
// NOT identity authority. They are deleted on ingress like the authority headers
// so no raw client copy survives, then re-injected by sanitizeSubScopes only for
// a validated principal and only after passing the cross-org guard. Unlike
// authorityHeaders they are not minted from the token; they must merely be proven
// non-foreign to the validated org.
var subScopeHeaders = []string{"X-Project-Id", "X-App-Id"}

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
// (which would break console's legitimate direct in-cluster BFF) — Phase-2. The
// ADMIN boundary (this fix's P0) is closed on every path regardless, because
// X-User-IsAdmin is NEVER restored from client input.
//
// FAIL MODE. If the validator can't verify a token (JWKS unreachable on a cold
// cache, issuer/audience misconfigured), the request resolves anonymous and BOTH
// planes fail SECURE. Admin fails closed (X-User-IsAdmin is never restored from
// client input), and — since F1 — the DATA plane fails closed too: it gates on a
// validated principal (clients/principal.Validated) and the anonymous request
// carries no X-User-Id, so the restored X-Org-Id is refused, not served. Never
// fails OPEN. The availability cost is bounded to COLD caches: the jwksCache is
// stale-on-error (a warm cache keeps validating through a transient JWKS outage),
// so only a from-cold JWKS failure degrades to anonymous-403.
func SanitizeIdentity(v *identityValidator, adminOrg string) zip.Handler {
	adminOrg = strings.TrimSpace(adminOrg)
	return func(c *zip.Ctx) error {
		req := c.Fiber().Request()

		// Capture the requested org + sub-scopes before stripping (admin org-switch
		// input + Phase-1 data passthrough for the org; cross-org validation input
		// for the sub-scopes), then delete every authority header AND every sub-scope
		// header, so nothing a client sent survives as identity OR scope. A client
		// org bearing a whitespace/control/format rune is refused here (not trimmed):
		// trimming would collapse "acme " onto "acme", and the injective tenant
		// boundary must never fold two distinct org identifiers into one.
		cliOrg := string(req.Header.Peek("X-Org-Id"))
		if OrgHasUnsafeRune(cliOrg) {
			cliOrg = ""
		}
		cliProject := strings.TrimSpace(string(req.Header.Peek("X-Project-Id")))
		cliApp := strings.TrimSpace(string(req.Header.Peek("X-App-Id")))
		for _, h := range authorityHeaders {
			req.Header.Del(h)
		}
		for _, h := range subScopeHeaders {
			req.Header.Del(h)
		}

		if claims := validatedPrincipal(c, v); claims != nil {
			// The org is taken verbatim from the validated principal and validated,
			// never trimmed: a whitespace/control/format-bearing owner is a
			// non-injective tenant identifier (TrimSpace / transport OWS-trim would
			// collapse "acme " onto "acme"), so it grants NO tenancy — the request
			// resolves org-less and every tenant() gate fails closed with 403,
			// rather than folding two IAM orgs onto one namespace.
			owner := claims.Owner
			if OrgHasUnsafeRune(owner) {
				owner = ""
			}
			if id := claims.userID(); id != "" {
				req.Header.Set("X-User-Id", id)
			}
			if claims.Email != "" {
				req.Header.Set("X-User-Email", claims.Email)
			}
			// effOrg is the org actually acted as: the switched-to org for a global
			// admin, else the principal's own owner. Sub-scopes are validated against
			// THIS org (a global admin viewing org X may legitimately carry X's
			// project; a project owned by neither is refused).
			var effOrg string
			switch {
			case claims.IsAdmin && owner != "" && owner == adminOrg && !isKMSMachinePrincipal(claims):
				// Verified GLOBAL admin: admin authority + honored org-switch. A KMS-sync
				// MACHINE principal (audience <owner>-platform-kms) is EXCLUDED here even
				// if it carries isAdmin=true: V6 accepts the machine audience for data
				// scope, but the machine path must never grant global admin, or an
				// admin-org machine token could read every tenant. It falls through to the
				// owner-scoped case below (org-scoped, no admin) — the audience widening
				// stays decoupled from admin inside cloud, not reliant on IAM's behavior.
				req.Header.Set("X-User-IsAdmin", "true")
				if cliOrg != "" {
					effOrg = cliOrg
				} else {
					effOrg = owner
				}
				req.Header.Set("X-Org-Id", effOrg)
			case owner != "":
				// Any other principal: pinned to their own org, never admin.
				effOrg = owner
				req.Header.Set("X-Org-Id", owner)
			}
			sanitizeSubScopes(c, effOrg, cliProject, cliApp)
			return c.Continue()
		}

		// No verified principal: admin authority is gone for good and the sub-scopes
		// stay stripped (no trusted org to validate a project against, and the data
		// plane gates on a validated principal anyway). Restore only the client org
		// for the Phase-1 data path (see residual note above).
		if cliOrg != "" {
			req.Header.Set("X-Org-Id", cliOrg)
		}
		return c.Continue()
	}
}

// IdentityMiddleware builds the identity trust-boundary middleware from cfg: it
// constructs the IAM JWT validator (trusted-issuer set, JWKS, audience allowlist)
// and returns SanitizeIdentity bound to the admin org. This is the ONE constructor
// for the boundary, so Serve and integration tests wire it identically — no second
// copy of the validator-construction glue to drift.
func IdentityMiddleware(cfg *Config) zip.Handler {
	return SanitizeIdentity(newIdentityValidator(cfg.IAMIssuer, cfg.JWKSURL, cfg.JWTAudiences, 0), cfg.AdminOrg)
}

// sanitizeSubScopes re-injects the org SUB-SCOPES (X-Project-Id, X-App-Id) for a
// VALIDATED principal, the raw client copies having been deleted on ingress. It
// is the project/app half of the trust boundary:
//
//   - X-Project-Id is re-injected only when it is NOT a cross-org claim
//     (projectIsForeign): the caller's OWN registered project, or an unregistered
//     free-form label, survives; a project REGISTERED to a different org is
//     refused (dropped). This is the membership-check-under-the-validated-org that
//     per-project scope always required.
//   - X-App-Id is a caller LABEL, not an isolation boundary: NO cloud subsystem
//     scopes access by it (git/security/eval scope by org + optional project, and
//     platform scopes apps by route params under the validated org), and an app is
//     always nested under an org-owned project, so the un-forgeable org column
//     bounds any mislabel to the caller's OWN subtree. It is forwarded as-is on the
//     validated path (a compute_usage attribution dimension) and dropped on the
//     anonymous path. If apps ever become a cross-org-consumed key, add a symmetric
//     appIsForeign guard here — the resolver already carries the org.
//
// With no effective org (an unsafe/absent owner) NOTHING is re-injected: an
// org-less request carries no scope.
func sanitizeSubScopes(c *zip.Ctx, org, project, app string) {
	if org == "" {
		return
	}
	req := c.Fiber().Request()
	if project != "" && !projectIsForeign(c.Context(), org, project) {
		req.Header.Set("X-Project-Id", project)
	}
	if app != "" {
		req.Header.Set("X-App-Id", app)
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
	// EMBED first-party bridge (last resort, cookie path). The go:embed console
	// (console.hanzo.ai → this binary) authenticates against its OWN in-process
	// IAM, which sets an OPAQUE, httpOnly session id (never a bearer) and stores
	// the user's IAM-minted access-token JWT SERVER-SIDE against that session. The
	// console's Next BFF token-minting routes are stripped by the static export, so
	// a browser request carries only the session cookie. Resolve it to that
	// server-stored JWT so the embed uses the SAME validated-JWT identity path as
	// every other client. Binds identity to the VALIDATED session: the client only
	// SELECTS which server-minted token to check (via an unguessable, httpOnly sid);
	// v.validate below still independently verifies sig/iss/aud/exp, and the session
	// never asserts identity itself. No-op on gateway-fronted binaries (a bearer is
	// already present, so this is never reached) and on binaries with no in-process
	// IAM session manager (web.GlobalSessions == nil).
	if tok == "" {
		tok = sessionAccessToken(c)
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

// sessionAccessToken returns the IAM access-token JWT the in-process IAM stored
// server-side for the caller's first-party session, or "" when there is no
// session manager, no session cookie, no session, or no stored token. The token
// is NOT trusted here — validatedPrincipal feeds it back through v.validate — so
// this only maps an opaque, httpOnly session id to the server-minted JWT bound to
// it. The session cookie name and store are the SAME ones clients/iamsvc wired
// into Beego's global session manager (web.BConfig.WebConfig.Session).
func sessionAccessToken(c *zip.Ctx) string {
	mgr := web.GlobalSessions
	if mgr == nil {
		return ""
	}
	name := web.BConfig.WebConfig.Session.SessionName
	if name == "" {
		return ""
	}
	sid := c.Fiber().Cookies(name)
	if sid == "" {
		return ""
	}
	store, err := mgr.GetSessionStore(sid)
	if err != nil || store == nil {
		return ""
	}
	tok, _ := store.Get(context.Background(), "accessToken").(string)
	return tok
}
