// Package principal is the ONE place the cloud data plane turns a request into a
// org. Every subsystem that reads or writes per-org data resolves its org
// through here, so the trust decision lives once and can never drift between six
// hand-rolled copies.
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
// verified (a JWT bearer or session cookie — an opaque hk-/sk- API key does NOT
// validate to a principal). So c.User() != "" is the authoritative "this request
// carries a validated principal" signal. It is the SAME gate the S3 data plane
// (clients/s3) and the audit trail (audit_middleware.go actorFromCtx) already
// ship. Every legitimate data-plane caller arrives through the console BFF, which
// mints a user-bound bearer, so the gate refuses ONLY the anonymous-forge path
// and breaks no real client — a data plane never serves an unauthenticated
// principal.
package principal

import (
	"strings"

	"github.com/zap-proto/zip"
)

// MaxOrgLen bounds the org key. The org is the validated IAM owner claim — a
// short DNS-ish label — so anything longer is malformed or hostile and is
// rejected before it can become a storage key or namespace.
const MaxOrgLen = 128

// DefaultProject is the reserved id of every org's default project. It is the ONE
// source of truth for the wire-contract value the gateway mints against
// (iamauth.DefaultProject): an absent X-Project-Id and the literal "default"
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
// Subsystems that resolve the plain org key use Org (which composes this).
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
func Org(c *zip.Ctx) (string, bool) {
	if !Validated(c) {
		return "", false // no validated principal — the restored X-Org-Id is untrusted
	}
	org := strings.TrimSpace(c.Org())
	if org == "" || len(org) > MaxOrgLen {
		return "", false
	}
	return strings.Clone(org), true
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

// BillingOrg resolves the org whose ledger PAYS for this request — the HOME org
// (Owner). It is the ONE "who pays" resolver: the edge gate, the AI meter, and the
// resource meter all key their balance CHECK and their DEBIT on it, so a platform
// SuperAdmin masquerading into another org spends from the admin org's balance and
// the debit lands on the admin ledger — never the org being acted on. DATA scope
// keeps using Org (the effective org); this splits "who pays" (home) from "whose
// data" (effective), which the old code conflated onto one org value.
//
// Gated on Validated like Org: an unvalidated request bills nothing (("", false)),
// so an off-gateway forge can neither probe nor drain a ledger. Falls back to the
// effective Org when the home header is absent — a normal caller has home==effective
// so the fallback is EXACT for them, and it preserves today's behavior on a gateway
// that has not yet minted X-User-Owner; only an admin org-switch differs, and that
// path always carries X-User-Owner once minted. Returns the resolved payer + true,
// or ("", false) when the request may not be billed.
func BillingOrg(c *zip.Ctx) (string, bool) {
	if !Validated(c) {
		return "", false
	}
	if owner := Owner(c); owner != "" {
		return owner, true
	}
	return Org(c) // rollout fallback: no home header yet ⟹ today's effective-org billing.
}

// HomeOrg is the bare-string form of BillingOrg for the in-handler resource meters
// (ResourceMeter.Gate/Meter/MeterUsage), which take an org string rather than the
// ctx. It returns the HOME org that PAYS (X-User-Owner, effective-org fallback), or
// "" when unvalidated. Call it ONLY after the caller has already resolved AND gated
// the effective org via Org (every resource handler does), so "" cannot occur on a
// live path; the meter also no-ops on an empty org, so an unexpected "" bills nothing
// rather than mis-billing. Use it for the billing key; keep Org for the data namespace.
//
// It was called Payer, which now means something else: hanzoai/account.Payer returns
// the ACCOUNT that pays, and this returns the ORG whose ledger holds it. Those are
// different values on the same request — a person in the shared signup org pays from
// account "hanzo/alice" held in ledger "hanzo" — so one name for both invited exactly
// the confusion that let the gate key the pool while the debit spent the person.
// An org names a ledger; an account names a wallet within it.
func HomeOrg(c *zip.Ctx) string {
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
// X-Project-Id from it SERVER-SIDE: the gateway (iamauth.Claims.MintedProject) and,
// on the off-gateway path, cloud.SanitizeIdentity (idClaims.mintedProject) — each
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

// BillingAccount resolves the caller's funding BillingAccount id — the GCP-style
// account (models/billingaccount) that pays for this request's usage. It mirrors
// Project: a zero-copy read of the gateway-minted X-Billing-Account-Id header (in
// production the gateway mints it from the validated IAM `billing_account` claim;
// off-gateway, SanitizeIdentity re-injects the caller's own value).
//
// It is an ATTRIBUTION hint ONLY. The account that is actually debited is resolved
// SERVER-SIDE by commerce from the org's ProjectBinding (resolveAccountId), never
// from this header — so a mislabelled account can only ever misattribute the
// caller's OWN spend within its own org and can NEVER redirect spend to another
// tenant's account. Empty when no account is in scope (the org-wide default pool).
// The value is CLONED because it is retained past the request for telemetry.
func BillingAccount(c *zip.Ctx) string {
	acct := strings.TrimSpace(c.Header("X-Billing-Account-Id"))
	if acct == "" {
		return ""
	}
	return strings.Clone(acct)
}
