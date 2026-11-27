// Package network mounts the Hanzo Cloud NETWORKING surface: the tenant's Hanzo
// Zero Trust footprint — the overlay network, its routers and its services —
// served as clean, org-scoped REST off the unified cloud binary and fronting the
// Hanzo Zero Trust controller (hanzoai/zt, an ZT-based fabric). It exists so
// the console's Networks, Service Mesh and Routers pages read REAL per-org ZT state
// from ONE place (api.hanzo.ai/v1/*) instead of rendering "not connected".
//
// The package answered to "zt" until the address took the name back. "zt" is an
// abbreviation of the UPSTREAM controller, not a word anyone says for the thing —
// the console pages say Networks, Routers and Service Mesh — and its build-name
// twin "zero-trust" carries a hyphen HIP-0139 §2.3 refuses. So the capability is
// network, singular by §2.2: an org has ONE overlay, and its routers and services
// are resources under it.
//
// This subsystem OWNS no ZT state — the controller does. It is a thin, tenant-scoped
// translator: it fronts the controller's Edge MANAGEMENT API (/edge/management/v1),
// filters every resource to the caller's org by the "org-<org>" role attribute, and
// re-shapes ZT objects into the exact JSON the console modules consume (types.go).
// It never fabricates: a mesh row is a real ZT edge service, a router row is a real
// edge-router with its real online status, and a network exists only when the org
// actually has edge-routers on the fabric.
//
// Surface (every route org-scoped by the validated principal; HIP-0026):
//
//	GET    /v1/network                 the org's ZT overlay network(s)  -> {networks:[networkView]}
//	GET    /v1/network/routers         the org's ZT edge-routers        -> {routers:[routerView]}
//	GET    /v1/network/services        the org's ZT edge services       -> {services:[meshView]}
//	POST   /v1/network/services        publish a service on the overlay -> {id,name,dns}
//	DELETE /v1/network/services/:id    take a published service off
//	GET    /v1/network/identities      the org's fabric identities      -> {identities:[identityView]}
//	POST   /v1/network/identities      ensure the caller's IAM identity -> {id,name,externalId}
//	DELETE /v1/network/identities/:id  remove an identity
//	GET    /v1/network/:id             one overlay network by id        -> networkView (404 if absent)
//
// and, beside the overlay, the way a person comes to be on it (access.go):
//
//	POST   /v1/network/invite/verify       is an invite code live?  ANONYMOUS     -> {valid}
//	POST   /v1/network/waitlist            join the waitlist (202)  ANONYMOUS     -> {status}
//	POST   /v1/admin/network/invites       issue invite codes, shown once  SuperAdmin -> {invites}
//	GET    /v1/admin/network/invites       list invites, never a code      SuperAdmin -> {data}
//	DELETE /v1/admin/network/invites/:id   revoke an invite                SuperAdmin
//
// THE WRITE HALF (identity.go, publish.go) is how a BYO device joins the org's
// overlay: ensure the IAM subject it logs in as is an identity, publish the
// service it hosts, and the org — and the cloud's own fleet — dials it by name. Every object written carries the same "org-<org>" attribute the read
// half filters by, so what a tenant writes is exactly what that tenant can
// later read, and nothing else.
//
// Networks maps to the fabric overview, its routers to the ZT edge-routers that ARE
// that overlay's nodes, and Service Mesh to ZT edge services — the three ZT concepts
// the three console pages need.
//
// SERVICES MOVED HERE FOR THE REASON THE ROUTERS DID. They were at /v1/mesh, a
// second top-level name for one capability, and a mesh row IS an edge service of
// this overlay — the resource belongs where its parent lives. /v1/mesh now 404s at
// every depth.
//
// THE ROUTERS LIVE UNDER THE NETWORK, and this is the whole reason the route moved.
// They were served at /v1/edge/nodes, which read as a top-level Hanzo product named
// "edge" and was not one: hanzoai/edge is the on-device inference runtime, a binary
// a customer runs on their own machine, and it has no cloud surface at all. Three
// more unrelated things wore the same word — the public catalogue cache, the
// gateway's CORS-and-rate-ceiling policy role, and a JWT audience list — so "edge"
// named a POSITION, never a product, and a prefix belongs to a product a customer
// calls. An edge-router is a node of an overlay network, so it is addressed as one.
// /v1/edge now 404s at every depth, which is correct rather than a missing product.
//
// TENANT ISOLATION. The org (principal.Org, the validated IAM owner) selects the
// "org-<org>" role attribute; the client lists the controller's resources and this
// subsystem filters to that role, so a caller can only ever read their OWN tenant's
// ZT footprint. The org is taken from the validated identity, never a client field.
//
// STEWARDS. Within an org, a plain member joins the overlay, dials what the org
// publishes, sees its own identity and leaves. Changing what the overlay serves
// or who else is on it — publishing, unpublishing, taking a role, listing or
// removing another member — is a steward's: an admin of the org, the platform,
// or the org's own machine client (see steward). An org's members are not always
// one team — a brand's sign-ups can share its org — and a host sits between the
// org's callers and the service, so hosting is never a plain member's to take.
//
// THE ANONYMOUS EXCEPTION. The two anonymous operations above are the one place this
// package takes no principal and never calls gate(): the person asking is not signed
// in, because the invite and the waitlist are how they come to be. They are declared
// open (spec.Open), read no org from the request (the records are the brand's, in the
// brand org's Base), and are bounded by per-caller, per-identifier and deployment-wide
// counts, since a code is a credential to be guessed at. They live here and not
// in marketing, beside the newsletter they resemble, because HIP-0139 §3 files a
// route under the capability it is the address of, and openapi/misfiled.txt only
// shrinks. An invite verifies without saying why it does not, and without consuming
// the code; the code is HNZ- and ten Crockford base32 characters, stored only as an
// HMAC under the KMS secret network/invite-hmac. Nothing server-side enforces an
// invite until redemption exists.
//
// The operator's half is a SuperAdmin's twice, once at the group and once in each op,
// and audited before it acts (invites_admin.go).
//
// FAIL-CLOSED. Absent the cloud's own IAM identity (iamurl.Machine), which is how
// it logs in to the controller, the subsystem mounts its full route space but
// every op returns an honest 503; it NEVER fabricates a network, service or node.
package network
