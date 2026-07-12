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

// OrgAdmin reports whether the caller is an ADMIN of the org it is acting in:
// EITHER the platform SuperAdmin (c.IsAdmin(), the X-User-IsAdmin the boundary
// mints only for owner == adminOrg) OR an admin of its OWN org (the
// X-User-IsOrgAdmin the boundary mints for any validated isAdmin principal). It is
// the ONE predicate the org-scoped admin panels gate on — a validated but
// NON-admin member of an org is NOT an OrgAdmin, so it is refused. Like c.IsAdmin()
// it reads an authority header the identity boundary (SanitizeIdentity) STRIPS on
// ingress and re-injects only from validated claims, so a client can never forge it.
func OrgAdmin(c *zip.Ctx) bool {
	return c.IsAdmin() || c.Header("X-User-IsOrgAdmin") == "true"
}

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

// Project resolves the caller's project — the org SUB-SCOPE that narrows WITHIN
// the validated org (a fleet registry shard, an ml namespace suffix, a metering
// attribution dimension). It mirrors c.Org() exactly: a zero-copy read of the
// gateway-minted X-Project-Id header (in production the gateway mints it from the
// validated IAM `project` claim; off-gateway, cloud.SanitizeIdentity re-injects it
// only when it is not a cross-org claim — so by the time it is read here it is
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

// ValidatedProject returns the caller's project AND whether that project is bound
// to a VALIDATED identity claim — the signal a per-scope spend cap uses to decide
// whether a project-scoped cap may HARD-enforce (402) or must DEGRADE to a soft
// warn (issue #70 project-spoof defense).
//
// Today it returns validated=FALSE. X-Project-Id survives SanitizeIdentity only as
// "the caller's OWN registered project OR an unregistered free-form label"
// (middleware_identity.go): cross-org projects are refused, but the caller still
// CHOOSES the label — it is not bound to a server-minted claim. So a caller can
// evade a project cap (tag spend with a different project) or, if it were hard,
// weaponize it. hanzo.id tokens carry owner/sub but NO project claim, so the
// gateway cannot mint a trustworthy X-Project-Id. Until IAM mints a project claim
// AND the gateway binds X-Project-Id to it server-side, project caps stay SOFT.
//
// This is the ONE lever: when that claim exists, return (project, true) here and
// project/service caps auto-harden across the edge gate and the resource meter.
// The ORG axis is always validated (owner claim); the SERVICE axis is
// server-derived (route/provider) — both are already trustworthy.
func ValidatedProject(c *zip.Ctx) (string, bool) {
	return Project(c), false
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
