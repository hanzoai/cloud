// Package principal is the guarantee that one org never reads another's data.
//
// It is the ONE place the cloud data plane turns a request into an org. Every
// subsystem that reads or writes per-org data resolves its org through here, so the
// trust decision lives once and can never drift between six hand-rolled copies.
//
// THE TRUST SIGNAL. zip.Ctx.Org()/User()/IsAdmin() read the X-Org-Id /
// X-User-Id / X-User-IsAdmin request headers. In production the gateway
// (hanzoai/gateway) is the sole minter of those headers — it strips any
// client-supplied copy and re-injects them from a validated IAM JWT (HIP-0026).
// But cloud-api is ALSO reachable WITHOUT the gateway in front: directly
// in-cluster (cloud-api.hanzo.svc:8000, used by console's BFF) and, until the
// ingress is locked down, on the public host api.cloud.hanzo.ai. The identity
// middleware (SanitizeIdentity, ../../middleware_identity.go) closes the
// forgeable-ADMIN hole on every path — X-User-IsAdmin is NEVER restored from
// client input — but on the bearer-less "Phase-1 data" path it RESTORES the
// client's raw X-Org-Id for data scoping while leaving X-User-Id EMPTY. So a
// request that presents an org but NO validated user is exactly the forge: an
// off-gateway caller sending `X-Org-Id: victim` with no credential, trying to
// read/write/delete another org's data.
//
// THE GATE. X-User-Id is set ONLY by the middleware, ONLY from a credential it
// verified (a JWT bearer or session cookie — an opaque pk-/sk- API key does NOT
// validate to a principal). So c.User() != "" is the authoritative "this request
// carries a validated principal" signal. It is the SAME gate the S3 data plane
// (clients/s3) and the audit trail (audit_middleware.go actorFromCtx) already
// ship. Every legitimate data-plane caller arrives through the console BFF, which
// mints a user-bound bearer, so the gate refuses ONLY the anonymous-forge path
// and breaks no real client — a data plane never serves an unauthenticated
// principal.
package principal

import (
	"context"
	"strings"

	"github.com/zap-proto/zip"
)

// MaxOrgLen bounds the org key. The org is the validated IAM owner claim — a
// short DNS-ish label — so anything longer is malformed or hostile and is
// rejected before it can become a storage key or namespace.
const MaxOrgLen = 128

// DefaultProject is the reserved id of every org's default project. It is the ONE
// source of truth for the wire-contract value the gateway mints against
// (the edge's default-project rule): an absent X-Project-Id and the literal "default"
// denote the SAME scope, so keyed surfaces (fleet refs, ml namespaces) keep
// today's un-suffixed keys for it — the backward-compatibility invariant.
const DefaultProject = "default"

// IsDefaultProject reports whether project is the org's default scope: the empty
// header (no project selected) or the literal DefaultProject. Keyed surfaces call
// this to decide whether to add a project segment, so "no project" and the
// default project map to exactly one — today's — key.
func IsDefaultProject(project string) bool {
	p := strings.TrimSpace(project)
	return p == "" || p == DefaultProject
}

// Validated reports whether the request carries a validated principal, i.e. the
// identity middleware set X-User-Id from a verified credential. This is the ONE
// predicate that separates a gateway-minted identity from a client-forged
// X-Org-Id on the bearer-less path.
//
// Subsystems that resolve the plain org key use Org (which composes this). A
// TYPED op cannot call this at all — it holds a context, not a request — so it
// reads ValidatedFrom, the same answer parked by WithValidated.
// Subsystems that derive their own PHYSICAL namespace from a route param or a
// normalized slug — KMS (route :org), S3 / provisioning / projects (DNS slug
// + admin bucket), ML (k8s namespace) — call Validated FIRST, then apply their
// own normalization, so the principal gate is never skipped.
func Validated(c *zip.Ctx) bool {
	return strings.TrimSpace(c.User()) != ""
}

// TWO ADMIN SCOPES, TWO PREDICATES, ONE FACT EACH — never conflate them
// (conflation is a privilege escalation).
//
//	IsSuperAdmin — platform sudo: the principal's ORG IS the reserved "admin" org
//	               (owner == adminOrg). Cross-tenant. The SAME equality IAM's
//	               canonical User.IsSuperAdmin() uses. `owner` is the org a
//	               principal BELONGS TO (<owner>/<name>), not a role.
//	IsOrgAdmin   — admin OF ONE'S OWN org: the IAM `isAdmin` bit. Org-scoped,
//	               self-service. NOT platform-privileged.
//
// A gate that admits either writes `IsSuperAdmin(c) || IsOrgAdmin(c)` — explicit,
// so the superset is visible at the gate instead of hidden inside a predicate.
// Both read authority headers SanitizeIdentity STRIPS on ingress and re-injects
// only from validated claims — unforgeable.

// IsSuperAdmin reports platform sudo: the caller is a member of the reserved
// "admin" org. SanitizeIdentity mints X-User-IsAdmin for exactly that identity,
// so c.IsAdmin() IS this predicate.
func IsSuperAdmin(c *zip.Ctx) bool { return c.IsAdmin() }

// IsOrgAdmin reports that the caller is an admin OF ITS OWN org (the IAM isAdmin
// bit, minted as X-User-IsOrgAdmin). It says NOTHING about platform sudo — a
// SuperAdmin is not implied. A validated but NON-admin member of an org is not one.
func IsOrgAdmin(c *zip.Ctx) bool { return c.Header("X-User-IsOrgAdmin") == "true" }

// Org resolves the caller's org — the org-isolation KEY — for the common
// verbatim case (crm, prompts, agents, functions, git, eval). It returns
// ("", false), and the caller MUST answer 403, unless BOTH hold:
//
//   - a validated principal is present (Validated), so the org is trustworthy
//     rather than a restored client header, and
//   - the org (c.Org(), the validated IAM owner) is non-empty and within
//     MaxOrgLen.
//
// The org is used VERBATIM — only trimmed, NEVER lowercased or truncated —
// because folding collapses DISTINCT owners ("acme" / "ACME" / a 32-char prefix)
// into one bucket, itself a cross-org break. The returned value is CLONED:
// c.Org() is a zero-copy view into the reused fasthttp request buffer, and the
// org key is retained past the request (DB rows, telemetry, async meters), so
// it must be a stable owned copy that cannot mutate to unrelated bytes.
//
// There is deliberately NO magic "admin" bucket here: a subsystem whose admin
// operates on per-org data carries an explicit org, so an empty org is a true
// 403. Subsystems that DO want an admin bucket (S3, provisioning) gate on
// Validated and add that fallback themselves.
func Org(c *zip.Ctx) (string, bool) { return OrgOf(c.User(), c.Org()) }

// OrgOf is the org-isolation decision ITSELF, over the only two facts it turns
// on: the validated user claim (empty ⇒ no validated principal, so the org that
// rode along is untrusted) and the org claim. Org reads those off a request; the
// internal plane (cloud.Ident, delegated in the envelope's capability slot) reads
// the SAME two headers off a capability. One rule, two readers — so a call that
// crosses the plane can never be granted an org key the HTTP boundary would have
// refused, which is the drift a second hand-rolled check would eventually be.
func OrgOf(user, org string) (string, bool) {
	if strings.TrimSpace(user) == "" {
		return "", false // no validated principal — the org claim is untrusted
	}
	org = strings.TrimSpace(org)
	if org == "" || len(org) > MaxOrgLen {
		return "", false
	}
	return strings.Clone(org), true
}

// ─────────────────────────────────────────────────────────────────────────────
// The MINTED principal — the boundary's own attestation
// ─────────────────────────────────────────────────────────────────────────────

// Principal is what the identity boundary MINTED for a request: the effective
// org it resolved and the user it verified. Both empty for an anonymous caller.
type Principal struct {
	Org  string
	User string
	// Subject is the token's `sub` VERBATIM. User is the canonical id and falls
	// back to preferred_username when a token carries no sub, which makes it an
	// attribution key rather than an identity key: two subjects can present the
	// same User. A consumer that RESOLVES A RECORD from the caller — an account
	// row, a membership — keys on this and refuses it empty, so it can never be
	// handed one identity's token and address another's row.
	Subject string
}

// mintedSlot names the request-local slot the boundary parks its attestation in.
// A request-local value is server-side state — it is not a header, it does not
// cross the wire, and there is no request a client can send that creates one.
// Unexported zero-size type, so only this package can mint or read it.
type mintedSlot struct{}

// Mint records the principal the identity boundary resolved. It is called by the
// boundary itself (cloud.SanitizeIdentity) and by nothing else.
//
// WHY THIS EXISTS BESIDE Validated. Validated reads X-User-Id, which is
// trustworthy only DOWNSTREAM of the boundary that strips and re-mints it. Most
// of cloud is downstream of it and Validated is the right question there. But a
// middleware is not guaranteed to be: the same handler is installed in the fused
// binary (behind the boundary) and in a hand-written plugin process (where the
// boundary may not be installed at all), and a gate that reads a header in the
// second case is reading whatever the client typed. So a gate whose correctness
// must not depend on its position asks THIS instead — a fact only the boundary
// can state, absent when the boundary did not run, which fails closed to
// anonymous rather than open to forged.
// EVERY field is CLONED. A value read off a request is a zero-copy view into
// the reused fasthttp buffer, and this one is retained past the read — it becomes
// a map key in the edge sensor and a column in a meter — so an un-owned copy
// would mutate into unrelated bytes on the next request through that worker.
// (A string used as a map key copies the header, never the backing array; Org
// clones for exactly this reason.)
func Mint(c *zip.Ctx, p Principal) {
	c.Fiber().Locals(mintedSlot{}, Principal{
		Org:     strings.Clone(p.Org),
		User:    strings.Clone(p.User),
		Subject: strings.Clone(p.Subject),
	})
}

// Minted returns the principal the boundary attested, and false when no boundary
// ran on this request. An anonymous request that DID pass a boundary returns
// (zero, true): "we looked, and there is nobody" is a different fact from "nobody
// looked", and only the caller knows which of them it can live with.
func Minted(c *zip.Ctx) (Principal, bool) {
	p, ok := c.Fiber().Locals(mintedSlot{}).(Principal)
	return p, ok
}

// orgKey names the request-scoped slot the validated org crosses the typed-op
// seam in. Unexported zero-size type, so only this package can mint or read one —
// the same unforgeability the header gate has.
type orgKey struct{}

// WithOrg parks the request's VALIDATED org on ctx so a TYPED op — which
// receives a context.Context and nothing else — can resolve it. It IS Org: the
// trust decision stays in that one function and this only carries its answer to
// the one seam that cannot call it. A request with no validated principal parks
// NOTHING, so the reader sees ("", false) rather than an empty org a query would
// then treat as a tenant.
func WithOrg(ctx context.Context, c *zip.Ctx) context.Context {
	org, ok := Org(c)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, orgKey{}, org)
}

// OrgFrom resolves the org WithOrg parked — the typed-op counterpart of Org, and
// the same value. The org is never an In field: an In field is caller-supplied,
// so a tenant key read from one is a cross-tenant read the caller asserted for
// itself.
// It has TWO readers, in this order, and they answer the same question through
// different doors:
//
//  1. the slot [WithOrg] parked, which the route middleware fills; and
//  2. zip's own caller, for the door that has NO route to run middleware on.
//
// The second is not a second rule. MCP's tools/call invokes an op DIRECTLY — no
// route, so no middleware, so nothing parks slot 1 — and until zip carried the
// caller over that door there was nothing else to read, which is why this package
// grew the slot at all. zip carries it now (caller_mcp_test.go), so the org is
// read back through [OrgOf]: the SAME two facts, the SAME decision, one more
// reader. A tool call therefore resolves the tenant a routed request would, and
// an unvalidated one still resolves nothing, because OrgOf refuses a blank user.
func OrgFrom(ctx context.Context) (string, bool) {
	if org, ok := ctx.Value(orgKey{}).(string); ok && org != "" {
		return org, true
	}
	c := zip.CallerOf(ctx)
	return OrgOf(c.User, c.Org)
}

// RequireOrg is [OrgFrom] composed with the ONE refusal, for the typed op that
// cannot serve without an org: the isolation key, or the 403 that says why not.
//
// It exists because fourteen subsystems had written it themselves — the same five
// lines under three names (`tenant`, `tenantOf`, `callerOf`), each with its own
// paragraph restating that the org is never an In field. That is one decision in
// fourteen places, and this package's own opening line says why that is the thing
// to avoid: the trust decision lives once "and can never drift between six
// hand-rolled copies". The decision did not drift. The REFUSAL was the copy —
// `zip.ErrForbidden("X-Org-Id required")`, a string a fifteenth subsystem would
// have had to spell correctly for its 403 to read like everyone else's.
//
// The org is never an In field: an In field is caller-supplied, so an org read
// from one is a cross-tenant read the caller asserted for itself. It is the org
// EXACTLY as the identity boundary minted it from the validated IAM owner claim —
// never lowercased, stripped or truncated, because folding collapses DISTINCT
// owners into one storage bucket, which is itself a cross-org break.
//
// It fails CLOSED off the HTTP path, where nothing parked an org and there is no
// caller to read: a CLI invoke resolves nothing and the op refuses, rather than
// serving the first request that arrives with no owner as if it had one.
//
// Ask [OrgFrom] instead where the absence is a BRANCH rather than a refusal, and
// [Validated] where the plane has no org to scope by and the gate is only whether
// the caller is signed in at all.
//
// There is NO request-shaped twin, unlike every other fact here ([Org]/[OrgFrom],
// [Validated]/[ValidatedFrom], [Brand]/[BrandFrom]). A raw handler holds a
// *zip.Ctx and asks [Org], which cannot be reached from a bare c.Context() —
// zip's caller finds no request behind one — so a twin would be a second function
// for the shape the fleet is migrating AWAY from. Investing an API in the raw
// handler is investing in the thing being deleted; a raw handler asks [Org] and
// writes its own refusal until it becomes a typed op.
func RequireOrg(ctx context.Context) (string, error) {
	org, ok := OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// validatedKey names the slot the WEAKER fact crosses the same seam in.
// Unexported zero-size type, exactly like orgKey.
type validatedKey struct{}

// WithValidated parks whether the request carried a validated principal AT ALL —
// the fact a gate turns on when there is no tenant to scope by. It IS Validated,
// carried to the one seam that cannot call it, exactly as WithOrg is Org.
//
// Two facts, TWO slots, because they are not the same question: OrgFrom composes
// this one AND an org, so it refuses a validated caller whose token names no home
// org (a machine token, or one minted before IAM's `orgs` claim — SanitizeIdentity
// mints X-User-Id for it and no X-Org-Id). A plane with per-org rows must refuse
// that caller; a plane whose reads are deployment-global platform facts (engine's
// shared runtime, o11y's infra health) gates on AUTHENTICATION and serves it. Both
// planes now answer from the context, so neither reaches for the request.
//
// An unvalidated request parks NOTHING, so the reader sees false rather than a
// fact it has to invert.
func WithValidated(ctx context.Context, c *zip.Ctx) context.Context {
	if !Validated(c) {
		return ctx
	}
	return context.WithValue(ctx, validatedKey{}, true)
}

// ValidatedFrom reports what WithValidated parked — the typed-op counterpart of
// Validated, and the same answer. FALSE off the HTTP path, where there is no
// request and so no attested caller: an op that gates on it refuses rather than
// serving an unauthenticated one.
// It reads the same two doors [OrgFrom] does, and for the same reason: the slot
// is filled by route middleware, and tools/call has no route. The fallback is
// [Validated]'s own predicate — a non-empty user — read off zip's caller instead
// of off a request, because over MCP there is a caller and no *zip.Ctx to ask.
//
// This is what made an op that gates on it unreachable AS A TOOL: the gate had
// been moved INTO the handler precisely so every door would reach it, but the
// fact it reads was still parked by the one door tools/call does not pass
// through, so the gate was unsatisfiable exactly where it was meant to work.
func ValidatedFrom(ctx context.Context) bool {
	if ok, _ := ctx.Value(validatedKey{}).(bool); ok {
		return true
	}
	return strings.TrimSpace(zip.CallerOf(ctx).User) != ""
}

// Owner resolves the caller's HOME org — the identity + BILLING anchor: the
// validated IAM `owner` claim, minted by the gateway (and by cloud's own identity
// boundary, SanitizeIdentity) as the X-User-Owner header. It is DISTINCT from Org
// (the EFFECTIVE / acted-on org, X-Org-Id): for a normal caller the two are
// identical, but a platform SuperAdmin acting in another org — an admin org-switch
// (owner == adminOrg, X-Org-Id = the switched-into org) — has Owner == "admin"
// while Org is the switched org. Empty when the home header is absent (a request
// with no validated principal, or a pre-rollout gateway that has not yet minted it).
// Bounded + cloned exactly like Org, for the same reason: the value is retained past
// the request as a ledger key, so it must be a stable owned copy.
func Owner(c *zip.Ctx) string {
	owner := strings.TrimSpace(c.Header("X-User-Owner"))
	if owner == "" || len(owner) > MaxOrgLen {
		return ""
	}
	return strings.Clone(owner)
}

// brandKey names the slot the vouching brand crosses the typed-op seam in.
// Unexported zero-size type, exactly like orgKey.
type brandKey struct{}

// Brand resolves WHICH BRAND'S IAM vouched for this principal — the brand the
// identity boundary resolved from the token's VERIFIED `iss` and minted as
// X-User-Brand (cloud.HeaderUserBrand), never a value a caller sent.
//
// It is the caller's half of a pair whose other half is the deployment's own
// brand. One cloud binary serves every brand's API host and trusts every brand's
// issuer, so those two are not the same fact, and a plane that keys rows by brand
// has to compare them rather than assume: `hanzo` the process and `lux.id` the
// signer disagreeing means one brand's org is about to be written into another's
// key space.
//
// ok is false when there is nothing to compare — no validated principal, or a
// principal with no issuer to resolve (an hk-/sk- key, minted by this
// deployment's own IAM). A caller must read that as "no second fact", never as
// a brand.
func Brand(c *zip.Ctx) (string, bool) {
	if !Validated(c) {
		return "", false
	}
	id := strings.TrimSpace(c.Header("X-User-Brand"))
	if id == "" {
		return "", false
	}
	return strings.Clone(id), true
}

// WithBrand parks the vouching brand on ctx so a TYPED op — which receives a
// context.Context and nothing else — can compare it with the deployment's. It IS
// Brand, carried to the one seam that cannot call it, exactly as WithOrg is Org.
// A request with nothing to compare parks NOTHING.
func WithBrand(ctx context.Context, c *zip.Ctx) context.Context {
	id, ok := Brand(c)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, brandKey{}, id)
}

// BrandFrom resolves what WithBrand parked — the typed-op counterpart of Brand,
// and the same value.
func BrandFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(brandKey{}).(string)
	return id, ok && id != ""
}

// BillingOrg resolves the org whose ledger PAYS for this request — the org the
// caller SELECTED, i.e. the effective org (Org). It is the ONE "who pays" resolver:
// the edge gate, the AI meter, and the resource meter all key their balance CHECK
// and their DEBIT on it.
//
// THE ORG IS THE PAYER OF RECORD. A person belongs to several orgs, picks one in the
// switcher, and that org's wallet funds the work — the same org whose data they are
// looking at, and the same org their top-up credited. Splitting "who pays" (home)
// from "whose data" (effective) is what made the switcher a lie: a member of `acme`
// could act in `acme` all day while every cent came out of their home org's books.
// The two are ONE value again, and the trust boundary is what makes that safe:
// SanitizeIdentity only ever sets X-Org-Id to an org the validated `orgs` claim says
// the caller belongs to (isMember), so an unselected, stale, or forged org can never
// become the effective org — and therefore can never become the payer.
//
// THE ONE EXCEPTION IS MASQUERADE, and it is not a selection. A platform SuperAdmin
// may act in ANY org, membership or not (that is what platform sudo means), so its
// effective org is not a statement about who should pay. It spends from its OWN
// books: the debit lands on the admin ledger, never on the org being inspected. The
// predicate needs no new header — the boundary already mints X-User-IsAdmin for
// exactly that identity, and a SuperAdmin acting at home has Owner == Org anyway.
//
// FAIL CLOSED. An unvalidated request bills nothing, so an off-gateway forge can
// neither probe nor drain a ledger; and an org that does not RESOLVE (absent, over
// MaxOrgLen) bills nothing either, rather than falling through to a default. There
// is no substitute payer: an unresolvable org must refuse, never charge someone else.
func BillingOrg(c *zip.Ctx) (string, bool) {
	org, ok := Org(c) // composes Validated; empty/oversized org ⟹ (", false) ⟹ refuse
	if !ok {
		return "", false
	}
	if IsSuperAdmin(c) {
		// Masquerade (or a SuperAdmin at home, where owner == org): spend own books.
		if owner := Owner(c); owner != "" {
			return owner, true
		}
	}
	return org, true
}

// Ledger is the bare-string form of BillingOrg for the in-handler resource meters
// (ResourceMeter.Gate/Meter/MeterUsage), which take an org string rather than the
// ctx. It returns the SELECTED org that PAYS, or "" when the request may not be
// billed. Call it ONLY after the caller has already resolved AND gated the effective
// org via Org (every resource handler does), so "" cannot occur on a live path; the
// meter also no-ops on an empty org, so an unexpected "" bills nothing rather than
// mis-billing. Use it for the billing key; keep Org for the data namespace.
//
// It is called Ledger, not Payer and no longer HomeOrg. Payer means something else
// (hanzoai/account.Payer returns the ACCOUNT that pays, and this returns the ORG
// whose ledger holds it), and HomeOrg became a lie the moment the SELECTED org
// started paying — a name that states the wrong fact is how a gate ends up keying
// one wallet while the debit spends another. An org names a ledger; an account names
// a wallet within it; this is the ledger.
func Ledger(c *zip.Ctx) string {
	if org, ok := BillingOrg(c); ok {
		return org
	}
	return ""
}

// Project resolves the caller's project — the org SUB-SCOPE that narrows WITHIN
// the validated org (a fleet registry shard, an ml namespace suffix, a metering
// attribution dimension). It mirrors c.Org() exactly: a zero-copy read of the
// server-minted X-Project-Id header (in production the gateway mints it from the
// validated IAM `project` claim; off-gateway, cloud.SanitizeIdentity mints it from
// the same claim, dropping a cross-org one — so by the time it is read here it is
// trustworthy, never a raw client value).
//
// The header is present iff a NON-default project is in scope, so an empty header
// resolves to DefaultProject — this is the backward-compatibility guarantee:
// existing single-project callers see "default" and keyed surfaces keep today's
// keys. The returned value is CLONED for the same reason Org clones: c.Org() /
// c.Header() are zero-copy views into the reused fasthttp request buffer, and the
// project is retained past the request as a storage-key / namespace component, so
// it must be a stable owned copy that cannot mutate to unrelated bytes.
//
// Unlike Org, Project does not gate on Validated: it is a scope NARROWING, not
// an authority. Every consumer AND-s it with the org resolved through Org (which
// does gate), so an unvalidated request is already refused at the org boundary
// before the project is ever used — the project can only ever narrow the caller's
// OWN org.
func Project(c *zip.Ctx) string {
	project := strings.TrimSpace(c.Header("X-Project-Id"))
	if project == "" {
		return DefaultProject
	}
	return strings.Clone(project)
}

// projectKey names the slot the NARROWING crosses the typed-op seam in.
// Unexported zero-size type, exactly like orgKey.
type projectKey struct{}

// WithProject parks the caller's project on ctx so a TYPED op — which receives a
// context.Context and nothing else — can resolve it. It IS Project, carried to
// the one seam that cannot call it, exactly as WithOrg is Org.
//
// It parks UNCONDITIONALLY, and that is the difference from WithOrg. Org is an
// AUTHORITY, so an unvalidated request must park nothing rather than an empty
// tenant a query would then honour. The project is a NARROWING — Project's own
// doc says it does not gate on Validated, because every consumer AND-s it with an
// org that does. Gating it here would state the authority twice and let the two
// statements disagree.
func WithProject(ctx context.Context, c *zip.Ctx) context.Context {
	return context.WithValue(ctx, projectKey{}, Project(c))
}

// ProjectFrom resolves what WithProject parked — the typed-op counterpart of
// Project, and the same value, with the SAME signature for the same reason the
// others share theirs: one fact, read from either side of the seam.
//
// A ctx with no request behind it answers DefaultProject, which is what Project
// answers for a request that names no project: "no narrowing". Neither is an
// authority — off the HTTP path OrgFrom reports no tenant and ValidatedFrom
// reports no principal, so the caller is already refused at the org boundary
// before a project is ever consulted.
func ProjectFrom(ctx context.Context) string {
	project, ok := ctx.Value(projectKey{}).(string)
	if !ok || project == "" {
		return DefaultProject
	}
	return project
}

// ProjectScope resolves the caller's project as a storage/filter KEY: "" for the
// org's DEFAULT project (which denotes the whole-org view — the default project ==
// the org's entire dataset), else the server-minted project slug. It is the ONE
// way a subsystem derives a project storage key, so the "default == empty ==
// whole org" convention lives in a single place (eval traces + metrics, o11y
// annotation queues all read it). Composes Project (server-minted, org-bound), so
// it is never a raw client value and only ever narrows the caller's OWN org.
func ProjectScope(c *zip.Ctx) string {
	p := Project(c)
	if IsDefaultProject(p) {
		return ""
	}
	return p
}

// ValidatedProject returns the caller's project AND whether that project is bound
// to a VALIDATED identity claim — the signal a per-scope spend cap uses to decide
// whether a project-scoped cap may HARD-enforce (402) or must DEGRADE to a soft
// warn (issue #70 project-spoof defense).
//
// It is claim-backed iff a validated principal carries a NON-default `project`
// claim. IAM now mints that claim next to `owner`, and BOTH minters bind
// X-Project-Id from it SERVER-SIDE: the gateway (the edge (hanzoai/authz/edge)) and,
// on the off-gateway path, cloud.SanitizeIdentity (idClaims.renderProject) — each
// stripping any client copy first and dropping a cross-org project. So a non-default
// X-Project-Id can only ever be a server-minted, validated scope; the caller can no
// longer CHOOSE its label to evade a project cap or, were it hard, weaponize it.
// That is the signal a project-scoped cap uses to HARD-enforce.
//
// The DEFAULT project stays SOFT (validated=false). An absent/default X-Project-Id
// means IAM minted NO project claim — the org has no project scope yet. IAM does not
// seed default projects, so today the claim is absent for EVERY org, and returning
// false here preserves exactly today's behavior (no surprise 402 on default-project
// spend). As an org's projects are seeded, its NAMED project caps auto-harden one
// org at a time. The ORG axis is always validated (owner claim) and the SERVICE axis
// is server-derived (route/provider), so only the PROJECT axis needs this signal.
func ValidatedProject(c *zip.Ctx) (string, bool) {
	project := Project(c)
	return project, Validated(c) && !IsDefaultProject(project)
}

// BillingAccount resolves the account that PAYS for this request — the IAM
// `billing_account` claim, as the `<kind>:<subject>` string ai/object.ParseAccount
// reads back. It mirrors Project: a zero-copy read of a SERVER-MINTED header.
//
// It is AUTHORITATIVE, not a hint. Both minters bind it from the validated claim
// and strip any client copy first — the gateway (the edge (hanzoai/authz/edge))
// and, on the in-cluster path, cloud's own SanitizeIdentity (idClaims.renderBillingAccount)
// — so a surviving value is IAM's signed statement of who pays, never a caller
// naming its own payer. Hand it to Payer as Credential.Account; Payer bounds it to
// the caller's own org and falls back when it is absent.
//
// Empty when IAM minted no account: a token from before the claim shipped, or an
// opaque pk-/sk- key that never carried claims. Payer's legacy rule answers for
// those, so an empty value bills the same account it always did — never nothing.
// The value is CLONED because it is retained past the request for telemetry.
func BillingAccount(c *zip.Ctx) string {
	acct := strings.TrimSpace(c.Header("X-Billing-Account-Id"))
	if acct == "" {
		return ""
	}
	return strings.Clone(acct)
}
