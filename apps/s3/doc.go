// Package s3 is object storage: your buckets and the files in them, with
// signed URLs for upload and download.
//
// It serves an org's buckets and objects at /v1/s3 — list, create, delete, and
// presigned upload/download URLs — over the shared S3 gateway.
//
// It is the DATA plane over that store — the companion to apps/provisioning,
// which is the CONTROL plane (allocate/list/drop the s3 RESOURCE at /v1/s3 and
// /v1/s3/:name).
//
//	GET    /v1/s3/health                              — real probe (503 fail-closed); public
//	GET    /v1/s3/buckets                             — list the caller's buckets;       JWT, org-scoped
//	POST   /v1/s3/buckets            {name}           — create a bucket;                  JWT, org-scoped
//	DELETE /v1/s3/buckets/:bucket                     — delete an EMPTY bucket;           JWT, org-scoped
//	GET    /v1/s3/buckets/:bucket/objects?prefix=&delimiter=/  — list objects (folders);  JWT, org-scoped
//	POST   /v1/s3/buckets/:bucket/objects  {key}      — presigned PUT url (upload);       JWT, org-scoped
//	GET    /v1/s3/buckets/:bucket/objects/+           — presigned GET url (download);     JWT, org-scoped
//	DELETE /v1/s3/buckets/:bucket/objects/+           — delete one object;                JWT, org-scoped
//
// ORG SCOPING — every tenant only ever sees or touches its OWN namespace. A
// bucket's PHYSICAL name is derived server-side from the caller's validated org
// as "o"<orgHash>_<name> (provisioning.PhysicalName — the SAME scheme the control
// plane allocates with, so a provisioned bucket is browsable here and vice-versa).
// The client speaks in FRIENDLY names ("photos"); the server maps friendly↔
// physical and NEVER trusts a client-supplied physical name. List filters to the
// caller's prefix; create/delete/object ops re-derive the physical name from the
// caller's org, so one tenant can never address another's bucket — the isolation
// boundary is by construction, not by a checked flag.
//
// FAIL-CLOSED — absent S3_ADMIN_* credentials the subsystem mounts
// health-only: /v1/s3/health is an honest 503 and every op returns 503. It never
// fabricates a bucket or object list.
//
// ROUTE ORDERING — registered as id "s3" with cloud.HealthOwner, at order 118
// (< provisioning's 120). Two independent concerns: (1) health — this subsystem
// serves its OWN fail-closed /v1/s3/health (Mount); cloud.HealthOwner makes Serve
// skip the generic always-ok /v1/<name>/health so it never shadows the real probe
// with a fake 200 (the same flag clients/kms and clients/paas use). (2) routing —
// Fiber v3 matches routes by an ORDERED scan and takes the first match, so the
// static GET /v1/s3/buckets and GET /v1/s3/health must register BEFORE
// provisioning's GET /v1/s3/:name (order 120) to win — hence order 118.
//
// RESIDUAL RISKS THIS SUBSYSTEM RIDES (documented after adversarial review; not
// fixable inside the subsystem, escalated to the platform):
//   - Single S3 identity: the S3 gateway uses ONE admin identity
//     (universe infra/k8s/storage/s3.yaml) for the whole binary. So the S3 LAYER
//     enforces no tenant boundary — isolation is 100% this subsystem's
//     org-prefixed naming + admit. The correct hardening is per-request
//     STS/session-policy or per-identity bucket-prefix restriction so the store
//     independently enforces the org boundary (defense in depth). Until then,
//     admit's cloud.Member check + the by-construction naming is the sole
//     boundary — kept minimal and auditable for that reason.
//   - Presign has no rate limit: minting is unthrottled (zip/middleware/ratelimit
//     is unwired in serve.go, platform-wide). The 5-minute TTL bounds a minted
//     capability's post-revocation lifetime; a per-route limiter is the platform
//     follow-up.
package s3
