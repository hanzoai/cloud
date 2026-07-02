// Package principal is the ONE place the cloud data plane turns a request into a
// tenant. Every subsystem that reads or writes per-org data resolves its tenant
// through here, so the trust decision lives once and can never drift between six
// hand-rolled copies.
//
// THE TRUST SIGNAL. zip.Ctx.Org()/User()/IsAdmin() read the X-Org-Id /
// X-User-Id / X-User-IsAdmin request headers. In production the gateway
// (hanzoai/gateway) is the sole minter of those headers — it strips any
// client-supplied copy and re-injects them from a validated IAM JWT (HIP-0026).
// But cloud-api is ALSO reachable WITHOUT the gateway in front: directly
// in-cluster (cloud-api.hanzo.svc:8000, used by console2's BFF) and, until the
// ingress is locked down, on the public host api.cloud.hanzo.ai. The identity
// middleware (SanitizeIdentity, ../../middleware_identity.go) closes the
// forgeable-ADMIN hole on every path — X-User-IsAdmin is NEVER restored from
// client input — but on the bearer-less "Phase-1 data" path it RESTORES the
// client's raw X-Org-Id for data scoping while leaving X-User-Id EMPTY. So a
// request that presents an org but NO validated user is exactly the forge: an
// off-gateway caller sending `X-Org-Id: victim` with no credential, trying to
// read/write/delete another tenant's data.
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

// MaxOrgLen bounds the tenant key. The org is the validated IAM owner claim — a
// short DNS-ish label — so anything longer is malformed or hostile and is
// rejected before it can become a storage key or namespace.
const MaxOrgLen = 128

// Validated reports whether the request carries a validated principal, i.e. the
// identity middleware set X-User-Id from a verified credential. This is the ONE
// predicate that separates a gateway-minted identity from a client-forged
// X-Org-Id on the bearer-less path.
//
// Subsystems that resolve the plain org key use Tenant (which composes this).
// Subsystems that derive their own PHYSICAL namespace from a route param or a
// normalized slug — KMS (route :org), S3 / provisioning / projectsvc (DNS slug
// + admin bucket), ML (k8s namespace) — call Validated FIRST, then apply their
// own normalization, so the principal gate is never skipped.
func Validated(c *zip.Ctx) bool {
	return strings.TrimSpace(c.User()) != ""
}

// Tenant resolves the caller's org — the tenant-isolation KEY — for the common
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
// into one bucket, itself a cross-tenant break. The returned value is CLONED:
// c.Org() is a zero-copy view into the reused fasthttp request buffer, and the
// tenant key is retained past the request (DB rows, telemetry, async meters), so
// it must be a stable owned copy that cannot mutate to unrelated bytes.
//
// There is deliberately NO magic "admin" bucket here: a subsystem whose admin
// operates on per-org data carries an explicit org, so an empty org is a true
// 403. Subsystems that DO want an admin bucket (S3, provisioning) gate on
// Validated and add that fallback themselves.
func Tenant(c *zip.Ctx) (string, bool) {
	if !Validated(c) {
		return "", false // no validated principal — the restored X-Org-Id is untrusted
	}
	org := strings.TrimSpace(c.Org())
	if org == "" || len(org) > MaxOrgLen {
		return "", false
	}
	return strings.Clone(org), true
}
