package cloud

// SanitizeIdentity — the in-binary identity trust boundary.
//
// THE PROBLEM. zip.Ctx.Org()/IsAdmin()/User() read the X-Org-Id / X-User-IsAdmin
// / X-User-Id request headers verbatim. In production the gateway
// (hanzoai/gateway) is the sole author of those headers: it strips any
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
// ADMIN IS SUPERADMIN, AND SUPERADMIN IS MEMBERSHIP. The cloud admin surfaces
// (global catalog writes, the literal "admin" org bucket, the CD plane) mean
// platform sudo, not "admin of my own org". Both facts are decided here, from the
// signed membership set, through the predicates authz publishes:
//
//	X-User-IsAdmin       ⟸ authz.Claims.Sudo — a HUMAN who is a MEMBER of
//	                       the reserved admin org, at ANY position in `orgs`.
//	X-User-IsOrgAdmin    ⟸ authz.Claims.OrgAdmin(effOrg) — admin/owner role in the
//	                       org the request ACTS in. Never platform authority.
//
// There is no `isAdmin` claim in this picture, in either direction. IAM mints one
// into NEITHER token — internal/oidc/jwt.go's Claims struct has no such field, and
// (*Signer).claims is the single place an Identity becomes a claim set, so the
// access token and the id_token carry the same claims but for aud/tokenType/nonce.
// The bit exists only as a user-row column that /v1/iam/userinfo and whoami report
// in a RESPONSE BODY. Anything here that appeared to read it was reading a claim
// that is never sent.

import (
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/brand"
	"github.com/zap-proto/zip"
)

// cookieTokenNames are the session-cookie names that may carry an IAM access
// token (mirrors edge.Cookie). hanzo_iam_token is the cookie the ai
// the embedded account layer SETS after login (ai/controllers/account.go iamTokenCookieName)
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

// HeaderUserBrand carries WHICH BRAND'S IAM vouched for the principal, resolved
// from the token's verified `iss` through the one brand registry (brand.ForIssuer).
//
// It is a SECOND fact, distinct from the deployment's own brand: one cloud binary
// serves every brand's API host and trusts every brand's issuer (trustedIssuers),
// so `cfg.Brand` says which brand this PROCESS is and this header says which
// brand this CALLER is. A plane that keys rows by brand — a tenant key, a
// derived-key salt — must be able to compare them, because "the process assumed
// hanzo" and "lux.id signed this" disagreeing is one org's rows landing in
// another's key space.
//
// It is minted only from validated claims and stripped on ingress like every
// other authority header, so it is never a value a caller chose. Absent for a
// principal that carries no issuer to resolve — an sk- API key, which this
// deployment's own IAM issued — and a consumer must treat absent as "no second
// fact to compare", never as a brand.
const HeaderUserBrand = "X-User-Brand"

// HeaderUserIsApp marks a validated principal that is an APPLICATION acting as
// itself — an organization's own machine identity, minted by the client_credentials
// grant.
//
// It is a KIND, not a role, and it is the fact only this boundary can state: the
// token proves it by SHAPE (appPrincipal), which is gone by the time anything
// downstream sees a header. It carries an organization the credential cannot choose
// — IAM mints an app token no membership set, so the org-switch below admits
// nothing and the effective org is always the application's own owner.
//
// It grants nothing on its own. An app holds NEITHER admin scope, deliberately:
// authz.Claims.OrgAdmin refuses every machine by construction, because an app is
// issued for a purpose and not handed an org's self-service surface. An endpoint
// whose act IS a purpose reads this to admit an org's own machine credential and
// bound it to that org — which is how a build carries the organization it publishes
// for instead of a shared secret that names none.
//
// Stripped on ingress with every other authority header and re-injected here from
// validated claims alone, so it is never a value a caller chose.
const HeaderUserIsApp = "X-User-IsApp"

// HeaderUserOrgs carries the ORGS THE PRINCIPAL BELONGS TO — the slugs of IAM's
// signed `orgs` membership set, comma-separated, home first as IAM writes it.
//
// X-Org-Id says which org this request ACTS IN, which is one org because a
// request has one tenant. This says which orgs the caller MAY act in, which is
// the different question an org switcher asks — and the only place that fact
// exists is the token, so without it a surface listing "your orgs" has to either
// re-validate the JWT itself (a second auth path) or ask IAM again (a hop for
// something already in hand). Hanzo Base's space could not list one Base per
// org for exactly that reason.
//
// It authorizes NOTHING on its own. Acting in an org still goes through the
// effective-org decision below, which honours a selection only when the same
// signed set contains it — so a reader of this header learns what to OFFER, and
// the boundary decides what is allowed.
//
// Same shape as X-User-Brand beside it: an issuer fact rendered by cloud for its
// own apps, minted only from validated claims and stripped on ingress, so it is
// never a value a caller chose. Absent for a principal carrying no membership set
// — an sk- key, a client_credentials machine — and a consumer must read absent as
// "no orgs to offer", never as "every org".
const HeaderUserOrgs = "X-User-Orgs"

// Two categories of name reach this boundary and they are RESTORED differently,
// which is the distinction to keep in mind reading the passes below.
//
// AUTHORITY is who the caller is — user, org, owner, brand, admin bits. A
// downstream may trust it precisely because a client copy never survives ingress;
// it is re-injected from a validated principal and from nothing else.
//
// An org SUB-SCOPE (X-Project-Id, X-App-Id, X-Billing-Account-Id) NARROWS within
// an org and is not authority, so sanitizeSubScopes restores it in a separate pass
// and per header: X-Project-Id is written from the validated `project` claim and
// then checked non-foreign to the effective org, X-App-Id is a caller attribution
// hint forwarded as-is, and X-Billing-Account-Id names WHO PAYS so it is minted
// from the validated `billing_account` claim. The raw client copy is never a
// source for any of them.
//
// Both categories are REMOVED the same way, by the one list below. They used to be
// two lists that also spelled what to remove, which is two places for one fact to
// go stale in.

// stripped is every identity name ingress DELETES, taken from the estate's own list
// rather than restated here.
//
// authz.Headers is defined as "every header the edge writes, and therefore every
// header it must strip" — the two being ONE list is what makes a forged identity
// header impossible rather than unlikely. Deriving from it keeps that property as the
// estate grows: a name authz adds is swept here the day it is added, with no second
// list to remember. The two categories above still differ in how a value is RESTORED,
// and they still say so; they no longer get to disagree about what is REMOVED.
//
// Two deliberate adjustments, each a fact about this binary rather than a copy:
//
//   - X-User-Brand and X-User-IsApp are cloud's own (which brand's IAM vouched for
//     the principal, and whether the principal is an application acting as itself),
//     so authz does not name them and this adds them.
//   - X-Request-Id is EXCLUDED. Alone among these it is a correlation id the caller
//     legitimately supplies and the edge propagates verbatim; deleting it would break
//     the trace support follows, and it authorizes nothing.
var stripped = func() []string {
	out := []string{HeaderUserBrand, HeaderUserIsApp, HeaderUserOrgs, zip.HeaderActedBy}
	for _, h := range append(append([]string{}, authz.Headers...), authz.Retired...) {
		if h != authz.HeaderRequestID {
			out = append(out, h)
		}
	}
	return out
}()

// SanitizeIdentity returns the identity-trust-boundary middleware.
//
// Per request:
//   - ALWAYS delete every header in stripped (a client copy never
//     survives — this alone kills X-User-IsAdmin forgery).
//   - Validate a Bearer / Basic / session-cookie JWT, if present:
//     SuperAdmin (authz.Claims.Sudo — a human MEMBER of the reserved admin
//     org, at any position in the signed `orgs` set). Membership IS the predicate;
//     the isAdmin bit is deliberately not a second term, and IAM does not mint one.
//     This test used to read `homeOrg == adminOrg`, i.e. `Orgs[0].Org` — a
//     POSITIONAL read that IAM's own ordering (home org always first) made true
//     only for a user whose ROW lives in the admin org, so every operator granted
//     admin-org membership was silently refused.
//     → X-User-IsAdmin=true; X-Org-Id = the requested org when present
//     (admin org-switch), else the home org.
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
// fails OPEN. The availability cost is bounded to COLD caches: the edge key cache is
// stale-on-error (a warm cache keeps validating through a transient JWKS outage),
// so only a from-cold JWKS failure degrades to anonymous-403.
// The reserved admin org is NOT a parameter. It is the ISSUER's constant — IAM
// hardcodes it (store.IsSuperAdmin: `owner == "admin"`, and the reserved-org set
// beneath it) and authz publishes it as authz.AdminOrg — so a consumer-side knob
// could only ever let cloud DISAGREE with the token contract it is reading. That
// was not hypothetical: this file's own test doc asserted "Hanzo pins it to
// 'hanzo'", which, had anyone set IAM_ADMIN_ORG that way, would have handed
// platform sudo to every member of the hanzo org while IAM considered none of
// them a SuperAdmin. Production never set it, so the default carried the truth by
// luck. A knob whose only reachable non-default setting is an estate-wide
// escalation is not configuration; it is a loaded footgun, and it is now gone.
func SanitizeIdentity(v *identityValidator) zip.Handler {
	return func(c *zip.Ctx) error {
		req := c.Fiber().Request()

		// Capture the requested org + the attribution sub-scopes before stripping
		// (admin org-switch input + Phase-1 data passthrough for the org; the app /
		// billing-account attribution hints), then delete every authority header AND
		// every sub-scope header, so nothing a client sent survives as identity OR
		// scope. X-Project-Id is NOT captured: it is written from the validated
		// `project` claim below (claims.renderProject), never from a client value. A
		// client org bearing a whitespace/control/format rune is refused here (not trimmed):
		// trimming would collapse "acme " onto "acme", and the injective org
		// boundary must never fold two distinct org identifiers into one.
		cliOrg := string(req.Header.Peek("X-Org-Id"))
		if principal.OrgHasUnsafeRune(cliOrg) {
			cliOrg = ""
		}
		cliApp := strings.TrimSpace(string(req.Header.Peek("X-App-Id")))
		for _, h := range stripped {
			req.Header.Del(h)
		}

		if claims := validatedPrincipal(c, v); claims != nil {
			// The org is taken verbatim from the validated principal and validated,
			// never trimmed: a whitespace/control/format-bearing owner is a
			// non-injective org identifier (TrimSpace / transport OWS-trim would
			// collapse "acme " onto "acme"), so it grants NO org-scoping — the request
			// resolves org-less and every org() gate fails closed with 403,
			// rather than folding two IAM orgs onto one namespace.
			// The USER's org, from the signed membership set — NOT the `owner` claim,
			// which carries the APPLICATION's org and is therefore chosen by whichever
			// app the caller authenticated through (see idClaims.homeOrg). Everything
			// below — the billing anchor, the SuperAdmin predicate, the effective org —
			// reads this ONE value, so both defects close together.
			owner := claims.homeOrg()
			if principal.OrgHasUnsafeRune(owner) {
				owner = ""
			}
			// FAIL CLOSED on a token that names no home org (no `orgs` claim: minted
			// before IAM v1.33.0, or a machine token). Falling back to claims.Owner
			// would reinstate the app-selected tenant this fix removes, so an
			// unresolvable home org grants NO scoping at all: the request continues
			// org-less and every org() gate refuses it. Logged, not silent — a real
			// legacy principal must be visible to us, not merely denied. Bounded by
			// token TTL: a re-auth mints the claim.
			// WARN, not Debug: the expected rate is ~zero (IAM has minted `orgs` since
			// v1.33.0 and live is far past it), so if this ever becomes noisy the noise
			// IS the alarm — it means legacy principals are real and being denied, which
			// we must learn immediately rather than infer from support tickets. Carries
			// the AUDIENCE so the offending CLIENT is named, not just the user: IAM sets
			// aud to the minting app's clientId (oidc/jwt.go audienceFor).
			if owner == "" {
				slog.Warn("identity: token names no home org (orgs claim absent or unsafe) — refusing org scope",
					"sub", claims.Subject, "aud", claims.Audience)
			}
			if id := claims.userID(); id != "" {
				req.Header.Set(authz.HeaderUser, id)
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
				req.Header.Set(authz.HeaderUserName, uname)
			}
			if claims.Email != "" {
				req.Header.Set(authz.HeaderUserEmail, claims.Email)
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
				req.Header.Set(authz.HeaderUserOwner, owner)
			}
			// The brand whose IAM actually minted this token, from the `iss` the
			// verifier just checked the signature against. It is stamped for every
			// validated principal whose issuer a brand claims, and for no other:
			// an issuer the registry does not know mints NOTHING, so a consumer
			// sees "no brand" rather than the default brand asserted over a
			// stranger's token.
			if id, ok := brand.ForIssuer(claims.Issuer); ok {
				req.Header.Set(HeaderUserBrand, id)
			}
			// The membership set itself, for the surfaces that must OFFER a choice
			// rather than make one. Minted before the effective-org decision, so it
			// describes the token and not the outcome of the switch below.
			if set := orgSlugs(claims.Orgs); set != "" {
				req.Header.Set(HeaderUserOrgs, set)
			}
			// effOrg is the org actually acted as: the switched-to org for a global
			// admin, else the principal's own owner. Sub-scopes are validated against
			// THIS org (a SuperAdmin viewing org X may legitimately carry X's
			// project; a project owned by neither is refused).
			var effOrg string
			switch {
			case owner != "" && platformSudo(claims):
				// SuperAdmin ⟺ a HUMAN who is a MEMBER of the reserved admin org — asked
				// through authz.Claims.Sudo, the predicate the ISSUER's own claim
				// package publishes. cloud does not re-derive it, because cloud re-deriving
				// it is what this arm got wrong.
				//
				// IT USED TO READ `owner == adminOrg`, i.e. claims.homeOrg(), i.e.
				// Claims.Orgs[0].Org — a POSITIONAL read. IAM's MemberOrgRefs always writes
				// the user's OWN org at index 0 and appends every granted membership after
				// it (iam internal/org/membership.go), so that test could only ever be
				// true for someone whose USER ROW lives in the admin org. An operator
				// provisioned into a brand org and then granted admin-org membership — the
				// deliberate, signed, revocable way operators are actually made — was
				// UNREACHABLE by it. z@hanzo.ai carries
				// orgs:[{hanzo,admin},{admin,admin},{lux,admin},{pars,admin},{zoo,admin}]
				// and was refused every platform surface, because `admin` sits at index 1.
				//
				// This WIDENS NOTHING. The authority was already signed by IAM and already
				// guarded on the write side: memberships.mayGrant refuses to create a
				// membership into a reserved org unless the caller is ALREADY a SuperAdmin,
				// on the stated grounds that it "seeds admin-org (SuperAdmin) tenancy". IAM
				// protects the grant as platform authority; this arm now honors it as
				// platform authority. The two agreeing is the fix — a grant the issuer
				// treats as sudo must not be inert at the resource server.
				//
				// Membership ALONE decides, deliberately — the isAdmin bit is NOT a second
				// term (IAM mints no such claim into ANY token; internal/oidc/jwt.go's
				// Claims struct has no such field, and one live token confirms it). The
				// admin org holds only SuperAdmins, provisioned in and never promoted, so
				// membership IS the fact; adding a role term would revoke sudo from an
				// admin-org user whose row carries isAdmin=false, which is a lockout, not a
				// hardening — TestMasqueradeSpendsOwnBooks pins exactly that principal.
				//
				// The HUMAN narrowing lives inside Sudo (Claims.Machine: an App
				// principal, or an empty membership set — a client_credentials token, which
				// IAM never mints `orgs` for). It is a POSITIVE human test, not a negated
				// machine one: this grants the only cross-tenant scope in the system, so an
				// unidentifiable principal must be refused rather than admitted by default.
				// A machine falls through to the owner-scoped arm below (org-scoped, not
				// super). Honored org-switch for the human admin.
				//
				// `owner != ""` stays as the ANCHOR guard, and is a separate question from
				// authority: an identity whose home org is unrepresentable (a whitespace/
				// control/format rune — OrgHasUnsafeRune) has no billing anchor and no
				// effective org to fall back to, so it fails closed here exactly as it does
				// in the arm below rather than acting with an empty X-Org-Id.
				req.Header.Set(authz.HeaderUserAdmin, "true")
				if cliOrg != "" {
					effOrg = cliOrg
				} else {
					effOrg = owner
				}
				req.Header.Set(authz.HeaderOrg, effOrg)
			case owner != "":
				// Any other principal acts in the org it SELECTED, provided the
				// validated token says it is a member of that org — the `orgs` claim
				// (IAM's signed membership set, home first). The org switcher is the
				// product: a person belongs to several orgs, picks one, and THAT org
				// is the payer of record (principal.BillingOrg reads this header). So
				// the selection has to survive the trust boundary, and membership is
				// the only thing that makes surviving safe.
				//
				// It is not a widening: the set is signed by IAM, so a caller can only
				// ever land on an org it already belongs to, and a claim-less token (a
				// legacy JWT, an sk- key, a client_credentials machine — IAM never
				// mints `orgs` for one) has an EMPTY set and stays pinned to home. A
				// selection outside the set is DISCARDED, not honored and not refused:
				// the request continues in the caller's own org, so a stale localStorage
				// selection after a membership is revoked reads the caller's own data
				// and bills the caller's own ledger — never someone else's.
				effOrg = owner
				if isMember(claims.Orgs, cliOrg) {
					effOrg = cliOrg
				}
				req.Header.Set(authz.HeaderOrg, effOrg)
			}
			// X-User-IsOrgAdmin marks a validated principal that is an admin OF ITS OWN
			// ORG — the IAM `isAdmin` bit (claims.IsAdmin). It is minted on the SAME
			// predicate in BOTH switch arms, so it covers a real SuperAdmin (admin of
			// the admin org) AND an ORG admin (admin of their own org). GuardScoped
			// requires it, so a validated but NON-admin member of an org is refused from
			// the org-scoped admin panels (the same denial an unvalidated caller gets),
			// closing the same-tenant over-visibility gap. It stays owner-scoped and safe:
			// a MACHINE principal (type == "application", or the owner-bound KMS-sync
			// audience) is EXCLUDED — mirroring the SuperAdmin guard above — so the machine
			// path grants NEITHER global NOR org admin, and the audience widening can never
			// be leveraged into an admin surface. Like every authorityHeader it is stripped
			// on ingress and re-injected ONLY here from validated claims, unforgeable.
			// Asked through authz.Claims.OrgAdmin — the same published predicate, for
			// the same reason: one reading of one claim, owned by the party that signs
			// it. It answers from the EFFECTIVE org's role in the signed membership set
			// (`orgs[].role`, folding owner into admin), and it SCOPES the legacy
			// `isAdmin` bit to the HOME org (`c.IsAdmin && org == c.Home()`).
			//
			// That scoping is the fix this line needed. It read `claims.IsAdmin ||
			// isOrgAdmin(...)` — an UNSCOPED disjunct, so a token carrying the bit would
			// have been org-admin in whatever org it switched INTO, not just its own.
			// The term is inert against IAM today (IAM mints no isAdmin claim at all),
			// which is precisely why it could sit there reading wrong: a dead term
			// cannot fail a test. Scoped to home, it is correct whether or not some
			// issuer ever starts minting it — forward-safe rather than accidentally-safe.
			//
			// Keyed on effOrg, not the home org: the bit must describe the org the
			// request ACTS in, so switching to an org you merely belong to never carries
			// admin across. The machine exclusion is inside OrgAdmin (Claims.Machine),
			// so a client_credentials identity is granted neither admin scope.
			if orgAdmin(claims, effOrg) {
				req.Header.Set(authz.HeaderUserOrgAdmin, "true")
			}
			// The credential's KIND, beside its role, because they answer different
			// questions and only this boundary can answer the first: an application
			// acting as itself is proved by the token's SHAPE (appPrincipal), which no
			// header downstream still carries. Written only where an org RESOLVED, so
			// the fact never arrives without the org it is about.
			if effOrg != "" && appPrincipal(claims) {
				req.Header.Set(HeaderUserIsApp, "true")
			}
			sanitizeSubScopes(c, effOrg, claims.renderProject(), cliApp, claims.renderBillingAccount())
			// The boundary's own attestation, parked where no client can reach it
			// (principal.Mint). The headers above are the contract everything
			// DOWNSTREAM reads; this is the fact a middleware reads when it cannot
			// prove it is downstream — see principal.Mint.
			principal.Mint(c, principal.Principal{
				Org: effOrg, User: claims.userID(), Subject: strings.TrimSpace(claims.Subject),
				// Carried from the credential IAM resolved, never from the request.
				Limit: claims.grant,
			})
			return c.Continue()
		}

		// No verified principal: admin authority is gone for good and the sub-scopes
		// stay stripped (no trusted org to validate a project against, and the data
		// plane gates on a validated principal anyway). Restore only the client org
		// for the Phase-1 data path (see residual note above).
		//
		// THE TRUSTED IN-PROC SERVICE CALLER arrives here too, and this line is the
		// whole of what it needs. `ai` is its own PROCESS, so it cannot see build.go's
		// in-process balanceReader and falls back to HTTP against /v1/billing/balance
		// bearing COMMERCE_SERVICE_TOKEN; apps/billing trusts that token
		// (account.IsServiceToken) but takes the org from X-Org-Id, which the strip
		// loop above deleted. That token is not a JWT, so validatedPrincipal is nil for
		// it and this restore runs unconditionally — which is why a second,
		// token-predicated copy of it above was dead code and is gone. Restoring the
		// org grants NO authority: no user, no admin, no roles, and the org itself is
		// still refused if it bears an unsafe rune.
		if cliOrg != "" {
			req.Header.Set(authz.HeaderOrg, cliOrg)
		}
		// The boundary RAN and found nobody. Recorded as such — an EMPTY attestation,
		// which is a different fact from no attestation at all. The org restored just
		// above is deliberately not in it: that value is the client's, kept for the
		// Phase-1 data path, and the whole point of this slot is that nothing a client
		// wrote ever enters it.
		principal.Mint(c, principal.Principal{})
		return c.Continue()
	}
}

// IdentityMiddleware builds the identity trust-boundary middleware from cfg: it
// constructs the IAM JWT validator (trusted-issuer set, JWKS, audience allowlist)
// and returns SanitizeIdentity bound to the admin org. This is the ONE constructor
// for the boundary, so Serve and integration tests wire it identically — no second
// copy of the validator-construction glue to drift.
func IdentityMiddleware(cfg *Config) zip.Handler {
	return SanitizeIdentity(newIdentityValidator(cfg.IAMIssuer, cfg.JWKSURL, 0))
}

// sanitizeSubScopes re-injects the org SUB-SCOPES (X-Project-Id, X-App-Id) for a
// VALIDATED principal, the raw client copies having been deleted on ingress. It
// is the project/app half of the trust boundary:
//
//   - project is the caller's validated `project` claim (claims.renderProject — empty
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
		req.Header.Set(authz.HeaderProject, project)
	}
	if app != "" {
		req.Header.Set(authz.HeaderApp, app)
	}
	// X-Billing-Account-Id names WHO PAYS, so it is written from the validated
	// `billing_account` claim (claims.renderBillingAccount) and never from a client
	// value — the raw copy is deleted on ingress and not restored here. It used to
	// be forwarded as-is, which was defensible only while it was a mere attribution
	// hint that no debit read. It is not one anymore: ai/object.Payer now resolves
	// the paying Account from this claim, so a restored client copy would be a caller
	// naming its own payer — the whole thing the claim exists to prevent. Absent when
	// IAM minted no account (a pre-claim token); Payer then falls back.
	if billingAccount != "" {
		req.Header.Set(authz.HeaderBillingAccount, billingAccount)
	}
}

// validatedPrincipal extracts a token (Bearer, Basic, then session cookie) and
// validates it. Returns nil when the credential is absent, opaque (a pk-/sk-
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
	// An opaque API key is not a JWT: resolve it to the same principal a JWT yields,
	// so key auth and session auth mint one identity. An unresolved key stays
	// anonymous (nil) — a bad key never grants trust.
	if isAPIKey(tok) {
		// A PUBLISHABLE key never becomes a principal. pk- ships in browser
		// bundles by design ("stored verbatim, safe to show"), so resolving it
		// here would hand every visitor a reading credential for the org that
		// owns it. It stays resolvable through OrgForKey — that is how the ingest
		// endpoint attributes a beacon to a tenant — but resolvable is not
		// authenticated.
		if IsPublishableKey(tok) {
			return nil
		}
		if v.keys == nil {
			return nil
		}
		return v.keys.resolve(c.Context(), tok)
	}
	// Parse ONCE. A ZAP socket replays its credential on every frame, so without
	// this the same token was signature-verified per call on an already
	// authenticated connection. A hit requires the same token bytes that
	// validated, so this is a memo of the verification — not a second way to be
	// trusted. See identity_cache.go.
	if claims := v.claims.get(tok, time.Now()); claims != nil {
		return claims
	}
	claims, err := v.validate(tok)
	if err != nil {
		return nil
	}
	v.claims.put(tok, claims)
	return claims
}

// sessionAccessToken used to map a first-party session cookie to the JWT the
// in-process IAM had stored server-side, by reading Beego's global session
// manager (web.GlobalSessions) that the retired iam-v1 embed wired up.
//
// That embed is retired. IAM v2 (github.com/hanzoai/iam) is zip-native on
// hanzoai/orm + hanzoai/sqlite and registers its surface directly on cloud's
// app, so nothing in this binary ever populates web.GlobalSessions — the
// function could only ever return "". It was dead code holding a whole beego
// module in the graph, along with the process-global config it drags in.
//
// Removed rather than kept "just in case": a session bridge to a manager that
// is never initialised is not a fallback, it is a lie about where sessions come
// from. If a first-party session ever needs to resolve to a token again, it
// resolves through IAM v2, not through a global in a retired framework.
func sessionAccessToken(*zip.Ctx) string { return "" }

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
// Presented is the credential the caller PUT ON THIS REQUEST, and empty when it
// put none there — the header half of callerToken, exactly, and nothing after it.
//
// The split is not cosmetic. A credential a caller presents cannot be supplied by
// a cross-site page (a browser will not set these headers for one), while an
// AMBIENT credential — a cookie, a same-origin session bridge — is sent by any page
// that can reach us. That is the whole anti-CSRF distinction, so apps/account asks
// this rather than deciding for itself what an explicit credential looks like.
//
// IT IS A CROSS-HEADER PRECEDENCE, and that is why it has to be one function
// rather than one parse applied twice. Basic is read from Authorization ONLY: a
// Basic value under X-Authorization is not a credential to this reader, so a gate
// that judged the two headers with the same per-header rule called such a request
// explicit while the boundary below fell through to the cookie — and the cookie
// then authenticated the write the gate had just excused. One question, one
// answer, one SHAPE.
func Presented(c *zip.Ctx) string {
	tok := bearerFromAuth(c.Header("Authorization"))
	if tok == "" {
		tok = bearerFromAuth(c.Header("X-Authorization"))
	}
	if tok == "" {
		tok = basicFromAuth(c.Header("Authorization"))
	}
	return tok
}

func callerToken(c *zip.Ctx) string {
	tok := Presented(c)
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
// An opaque API key (pk-/sk-) is NOT a relayable bearer -- an OIDC target cannot
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
