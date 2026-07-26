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
// It is ONE place; every existing IsAdmin()/Org() reader (pricing admin
// catalog + /v1/pricing/sync, provisioning, ml, eval, plan) becomes
// trustworthy without touching a single handler.
//
// ADMIN IS SUPERADMIN. The gateway mints X-User-IsAdmin from the JWT `isAdmin`
// bool, which IAM also sets true for ORG admins (an org owner). The cloud admin
// surfaces (global catalog writes, the literal "admin" org bucket) mean
// SuperAdmin. So the admin authority here is granted ONLY to a validated
// principal whose org IS the admin org (owner == adminOrg — IAM's IsSuperAdmin),
// matching the gateway's admin-guard. An org admin gets NO admin authority.

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/hanzoai/beego/v2/server/web"
	"github.com/zap-proto/zip"
)

// OrgHasUnsafeRune reports whether s carries any whitespace, control, or
// zero-width/format rune — the class that defeats the injectivity of the
// org→org map. strings.TrimSpace (and fasthttp's own header-value OWS
// trimming) silently drop such runes at the edges, so two DISTINCT IAM org
// names ("acme" vs "acme ", or an NBSP/ZWSP variant) would collapse onto ONE
// org-<slug> namespace / image ref — a cross-org fold. The identity trust
// boundary REFUSES to grant org-scoping from an org bearing one of these (fail
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
// token (mirrors iamauth.CookieToken). hanzo_iam_token is the cookie the ai
// (casibase) layer SETS after login (ai/controllers/account.go iamTokenCookieName)
// — it MUST be read here too, or the embedded console (whose browser holds only
// that httpOnly cookie, no Authorization header) resolves to no principal and
// every org-scoped /v1 endpoint (agents/gpus/machines/platform/orgs/…) 403s
// "X-Org-Id required". The JWT is present in the browser; this reads the right name.
//
// __Host-hanzo_iam_token is FIRST and is the name a first-party sign-in should
// mint (clients/deploy does). The __Host- prefix is enforced by the browser: such
// a cookie may only be set Secure, Path=/, and WITHOUT a Domain attribute, so a
// sibling *.hanzo.ai host cannot set a Domain=.hanzo.ai cookie of the same name to
// shadow it. The unprefixed names stay for the clients that already mint them (ai's
// account.go, clients/team), and are read only if no un-shadowable cookie is present.
var cookieTokenNames = []string{"__Host-hanzo_iam_token", "hanzo_iam_token", "iam_access_token", "access_token", "hanzo_token"}

// authorityHeaders are the identity/authority headers the gateway mints and the
// ONLY ones a downstream may trust. The sanitizer deletes every one on ingress
// so nothing a client sent survives as identity, then re-injects from a
// validated principal.
//
// Deliberately NOT in this list: org SUB-SCOPES (X-Project-Id, X-App-Id,
// X-Environment). A sub-scope NARROWS within an org; it is not identity authority,
// so it is handled in a separate pass (sanitizeSubScopes, keyed on
// subScopeHeaders): every raw client copy is deleted on ingress, then X-Project-Id
// is RE-MINTED from the validated `project` claim (claims.mintedProject) exactly
// like X-Org-Id from `owner`, still checked non-foreign to the effective org
// (projectIsForeign — org_scope.go refuses a project REGISTERED to a DIFFERENT org,
// which also drops an admin's own-org project when a SuperAdmin views another
// org), and dropped on the anonymous path. The raw client X-Project-Id is NEVER a
// source. So after this pass X-Project-Id is a TRUSTWORTHY server-minted scope,
// which is exactly why principal.ValidatedProject reports it claim-backed and per-
// project spend caps HARD-enforce. The native evals subsystem, which scopes
// exclusively by c.Org() and ignores X-Project-Id, is unaffected.
var authorityHeaders = []string{
	"X-User-Id",
	"X-Org-Id",
	"X-Roles",
	"X-User-Permissions",
	"X-User-Email",
	"X-Phone-Number",
	"X-User-IsAdmin",
	"X-User-IsOrgAdmin",
	// X-User-Owner is the HOME org (the validated `owner` claim) — the identity +
	// BILLING anchor, minted below DISTINCT from X-Org-Id (the effective/acted-on
	// org). Stripped on ingress like every authority header so a client can never
	// forge who pays, then re-injected only from validated claims.
	"X-User-Owner",
	// Legacy identity aliases an attacker might try; none are org sub-scopes.
	"X-User-Role",
	"X-User-Roles",
	"X-User-Name",
	"X-Org",
}

// subScopeHeaders are org SUB-SCOPES — narrowings WITHIN an org, NOT identity
// authority. All are deleted on ingress like the authority headers so no raw client
// copy survives, then re-injected by sanitizeSubScopes only for a validated
// principal:
//   - X-Project-Id is MINTED from the validated `project` claim (claims.mintedProject),
//     exactly like X-Org-Id from `owner`, then still checked non-foreign to the
//     effective org (defense in depth; it also drops an admin's own-org project when
//     a SuperAdmin views another org). The raw client copy is never a source.
//   - X-App-Id / X-Billing-Account-Id are caller attribution hints (no isolation
//     boundary): forwarded as-is on the validated path, dropped when anonymous.
var subScopeHeaders = []string{"X-Project-Id", "X-App-Id", "X-Billing-Account-Id"}

// SanitizeIdentity returns the identity-trust-boundary middleware.
//
// Per request:
//   - ALWAYS delete every header in authorityHeaders (a client copy never
//     survives — this alone kills X-User-IsAdmin forgery).
//   - Validate a Bearer / Basic / session-cookie JWT, if present:
//     SuperAdmin (claims.isAdmin && owner == adminOrg)
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
// caller can still SELECT an org for DATA reads. Closing that needs the data
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

		// Capture the requested org + the attribution sub-scopes before stripping
		// (admin org-switch input + Phase-1 data passthrough for the org; the app /
		// billing-account attribution hints), then delete every authority header AND
		// every sub-scope header, so nothing a client sent survives as identity OR
		// scope. X-Project-Id is NOT captured: it is minted from the validated
		// `project` claim below (claims.mintedProject), never from a client value. A
		// client org bearing a whitespace/control/format rune is refused here (not trimmed):
		// trimming would collapse "acme " onto "acme", and the injective org
		// boundary must never fold two distinct org identifiers into one.
		cliOrg := string(req.Header.Peek("X-Org-Id"))
		if OrgHasUnsafeRune(cliOrg) {
			cliOrg = ""
		}
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
			// non-injective org identifier (TrimSpace / transport OWS-trim would
			// collapse "acme " onto "acme"), so it grants NO org-scoping — the request
			// resolves org-less and every org() gate fails closed with 403,
			// rather than folding two IAM orgs onto one namespace.
			owner := claims.Owner
			if OrgHasUnsafeRune(owner) {
				owner = ""
			}
			if id := claims.userID(); id != "" {
				req.Header.Set("X-User-Id", id)
			}
			// X-User-Name is the IAM USERNAME (the `name` half of <owner>/<name>),
			// stamped DISTINCT from X-User-Id (the UUID subject). The gateway path
			// historically minted X-User-Id==name so <owner>/X-User-Id resolved; the
			// in-binary direct-Bearer path stamps X-User-Id==sub (a UUID), which broke
			// IAM's mint-user-keys/get-user (owner/uuid → "password or code is
			// incorrect"). resolveCaller prefers this header for the owner/name key and
			// falls back to X-User-Id, so both paths mint keys correctly. Like every
			// authorityHeader it is stripped on ingress (line ~97) and re-injected here
			// ONLY from validated claims — never a client value.
			if uname := claims.username(); uname != "" {
				req.Header.Set("X-User-Name", uname)
			}
			if claims.Email != "" {
				req.Header.Set("X-User-Email", claims.Email)
			}
			// X-User-Owner is the HOME org — the validated `owner` claim, minted
			// here DISTINCT from X-Org-Id (the effective org set below). It is the
			// identity + BILLING anchor: for a normal principal it equals X-Org-Id,
			// but for a SuperAdmin org-switch it stays the admin org while X-Org-Id
			// becomes the switched-into org — so the billing gate + debit (which key
			// on principal.Owner/BillingOrg) always land on the admin ledger, never
			// the org being acted on. Minted for EVERY validated principal, before the
			// switch, so it is independent of the effective-org decision. An unsafe/
			// empty owner mints nothing (billing then fails closed with no home org).
			if owner != "" {
				req.Header.Set("X-User-Owner", owner)
			}
			// effOrg is the org actually acted as: the switched-to org for a global
			// admin, else the principal's own owner. Sub-scopes are validated against
			// THIS org (a SuperAdmin viewing org X may legitimately carry X's
			// project; a project owned by neither is refused).
			var effOrg string
			switch {
			case owner != "" && owner == adminOrg && !isKMSMachinePrincipal(claims):
				// SuperAdmin ⟺ the principal's org IS the reserved admin org. ONE fact,
				// the SAME equality IAM's canonical User.IsSuperAdmin() uses
				// (user.Owner == conf.AdminOrg) — cloud does not add a second signal.
				// Honored org-switch. A KMS-sync MACHINE principal (audience
				// <owner>-platform-kms) is EXCLUDED: V6 accepts the machine audience for
				// data scope, but the machine path must never grant SuperAdmin, or an
				// admin-org machine token could read every org. It falls through to the
				// owner-scoped case below (org-scoped, not super).
				req.Header.Set("X-User-IsAdmin", "true")
				if cliOrg != "" {
					effOrg = cliOrg
				} else {
					effOrg = owner
				}
				req.Header.Set("X-Org-Id", effOrg)
			case owner != "":
				// Any other principal: pinned to their own org, never SuperAdmin.
				effOrg = owner
				req.Header.Set("X-Org-Id", owner)
			}
			// X-User-IsOrgAdmin marks a validated principal that is an admin OF ITS OWN
			// ORG — the IAM `isAdmin` bit (claims.IsAdmin). It is minted on the SAME
			// predicate in BOTH switch arms, so it covers a real SuperAdmin (admin of
			// the admin org) AND an ORG admin (admin of their own org). GuardScoped
			// requires it, so a validated but NON-admin member of an org is refused from
			// the org-scoped admin panels (the same denial an unvalidated caller gets),
			// closing the same-tenant over-visibility gap. It stays owner-scoped and safe:
			// a KMS-sync MACHINE principal (audience <owner>-platform-kms) is EXCLUDED —
			// mirroring the SuperAdmin guard above — so the machine path grants NEITHER
			// global NOR org admin, and V6's audience widening can never be leveraged into
			// an admin surface. Like every authorityHeader it is stripped on ingress and
			// re-injected ONLY here from validated claims, so a client can never forge it.
			if claims.IsAdmin && !isKMSMachinePrincipal(claims) {
				req.Header.Set("X-User-IsOrgAdmin", "true")
			}
			sanitizeSubScopes(c, effOrg, claims.mintedProject(), cliApp, claims.mintedBillingAccount())
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
//   - project is the caller's MINTED `project` claim (claims.mintedProject — empty
//     for the default project, so the header stays absent ⟺ default). It is
//     re-injected only when NON-foreign to org (projectIsForeign): the caller's own
//     claim survives; a project REGISTERED to a different org is refused (dropped),
//     which drops an admin's own-org project when a SuperAdmin (effOrg = the
//     switched-to org) views another org. The header is now server-minted, so a
//     surviving X-Project-Id is claim-backed — principal.ValidatedProject trusts it.
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
func sanitizeSubScopes(c *zip.Ctx, org, project, app, billingAccount string) {
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
	// X-Billing-Account-Id names WHO PAYS, so it is minted from the validated
	// `billing_account` claim (claims.mintedBillingAccount) and never from a client
	// value — the raw copy is deleted on ingress and not restored here. It used to
	// be forwarded as-is, which was defensible only while it was a mere attribution
	// hint that no debit read. It is not one anymore: ai/object.Payer now resolves
	// the paying Account from this claim, so a restored client copy would be a caller
	// naming its own payer — the whole thing the claim exists to prevent. Absent when
	// IAM minted no account (a pre-claim token); Payer then falls back.
	if billingAccount != "" {
		req.Header.Set("X-Billing-Account-Id", billingAccount)
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
	tok := callerToken(c)
	if tok == "" {
		return nil
	}
	// THE READ-GATE. A write-only PUBLISHABLE key (pk-) is NEVER a principal. It is
	// designed to ship in client JS, so it must be read-incapable: refuse it HERE,
	// before the key resolver, so it can never mint an identity header (X-User-Id /
	// X-Org-Id) and therefore can never satisfy ANY read gate anywhere in the binary
	// (every read gates on a validated principal — clients/principal.Validated). Its
	// ONLY power is attributing an INGEST to its org (eventTenant → OrgForKey → IAM
	// resolve-key). This short-circuit is load-bearing and structural: even if the key
	// resolver would resolve a pk- to a full principal, the boundary refuses it first,
	// so a public browser key can never read — on the direct path or any other.
	if publishableKey(tok) {
		return nil
	}
	// An opaque API key is not a JWT: resolve it to the same principal a JWT yields,
	// so key auth and session auth mint one identity. An unresolved key stays
	// anonymous (nil) — a bad key never grants trust.
	if isAPIKey(tok) {
		if v.keys == nil {
			return nil
		}
		return v.keys.resolve(c.Context(), tok)
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
// it. The session cookie name and store are the SAME ones clients/iam wired
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

// sessionBridgeSameOrigin reports whether the request may use the ambient-cookie
// session bridge (RED H3). A legitimate embed request is same-origin (the SPA calls
// its OWN host); a cross-site OR sibling-subdomain (same-site) request that merely
// rides the victim's session cookie is refused, so the bridge can never be a CSRF
// vector even on a state-changing GET. Prefers Sec-Fetch-Site (browser-set,
// JS-unforgeable); falls back to an Origin/Referer host==Host check when it is absent
// (a modern browser always sends Sec-Fetch-Site, so the fallback is only for exotic
// clients — which, lacking the httpOnly cookie, can't reach the bridge anyway).
func sessionBridgeSameOrigin(c *zip.Ctx) bool {
	if sfs := c.Header("Sec-Fetch-Site"); sfs != "" {
		return sfs == "same-origin" || sfs == "none"
	}
	host := c.Fiber().Hostname()
	for _, h := range []string{c.Header("Origin"), c.Header("Referer")} {
		if h == "" {
			continue
		}
		u, err := url.Parse(h)
		if err != nil || !strings.EqualFold(u.Host, host) {
			return false
		}
	}
	return true
}

// callerToken resolves the credential token a request presents, in the SAME
// precedence SanitizeIdentity trusts, and returns it UNCHANGED (validation is the
// caller's job). It is the ONE token-resolution both the identity boundary
// (validatedPrincipal) and the downstream bearer relay (CallerBearer) read, so the
// two can never disagree on which credential identifies the caller:
//
//  1. request Bearer (Authorization), then X-Authorization Bearer, then Basic
//     password (the go/.netrc proxy idiom),
//  2. a JWT-bearing session cookie (cookieTokenNames),
//  3. EMBED first-party bridge (last resort). The go:embed console
//     (console.hanzo.ai -> this binary) authenticates against its OWN in-process
//     IAM, which sets an OPAQUE, httpOnly session id (never a bearer) and stores the
//     user's IAM-minted access-token JWT SERVER-SIDE against that session. The
//     console's Next BFF token-minting routes are stripped by the static export, so
//     a browser request carries only the session cookie; resolve it to that
//     server-stored JWT so the embed uses the SAME identity path as every other
//     client. GATED SAME-ORIGIN (RED H3): the session cookie is ambient, so the
//     bridge fires ONLY for a same-origin request -- a cross-site / sibling-subdomain
//     request that merely rides the cookie can never reach it. No-op with no
//     in-process IAM session manager (web.GlobalSessions == nil).
//
// The returned token is NOT trusted here: validatedPrincipal feeds it through
// v.validate (sig/iss/aud/exp), and CallerBearer relays it to a target that
// re-validates it. Empty when the request carries no credential.
func callerToken(c *zip.Ctx) string {
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
	if tok == "" && sessionBridgeSameOrigin(c) {
		tok = sessionAccessToken(c)
	}
	return tok
}

// CallerBearer returns the caller's validated JWT bearer for RELAY to a downstream
// org-scoped service (e.g. the DNS control plane) that authorizes on the caller's
// OWN identity and derives the org from the token's `owner` claim. It returns the
// SAME token SanitizeIdentity validated the principal with (callerToken), UNCHANGED
// -- cloud substitutes NO service credential of its own, so tenant isolation carries
// across the hop: a caller in org A relays an org-A token and can reach only org A.
//
// An opaque API key (hk-/sk-/...) is NOT a relayable bearer -- an OIDC target cannot
// validate it and forwarding it would leak the key -- so it returns "". Empty when
// the request carries no validatable bearer; the relay then sends no Authorization
// and the downstream fails closed on its own gate.
func CallerBearer(c *zip.Ctx) string {
	tok := callerToken(c)
	if tok == "" || isAPIKey(tok) {
		return ""
	}
	return tok
}
