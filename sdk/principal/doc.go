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
// middleware (SanitizeIdentity, ../../middleware_identity.go) deletes every
// authority header a client sent and writes X-User-Id, X-Org-Id and the admin
// bits back only for a principal it validated. So an org is authority only
// beside a validated user, and a request that presents an org but NO validated
// user — one a process without the boundary received, say — names nobody.
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
// BESIDE IT, two more facts about the same principal live here because both are
// decided from it and neither can stand without it. orgns.go is the one way a
// validated org becomes the namespace its stores are opened under
// (OrgNamespace refuses what would collide or escape the data directory).
// ingress.go names what a caller may not assert about itself: the authority
// headers minted at the ingress, and the one list of every identity header that
// ingress deletes before it mints any — shared by the identity boundary every
// program runs and the host's GraphQL relay, so two lists cannot drift.
package principal
