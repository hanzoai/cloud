// Package base is managed Hanzo Base: a hosted backend for your app —
// collections, records, access rules and sign-in.
//
// It serves that engine per org at /v1/base, plus the platform's public
// waitlist at /v1/waitlist.
//
// It is the in-binary replacement for the standalone `ghcr.io/hanzoai/superbase`
// pod, whose whole job was `base.New()` + serve. cloud already links
// github.com/hanzoai/base, so it runs the SAME engine in-process across TWO
// orthogonal lanes:
//
//	LANE 1 — the viral waitlist (GTM launch surface). ONE platform Base app
//	carries the waitlist plugin; its /v1/waitlist/* routes are PUBLIC (a signup
//	surface has no principal to scope by) and platform-owned (one waitlist per
//	brand, not customer data). console reads it via WAITLIST_URL=…/v1/waitlist.
//
//	LANE 2 — managed Base hosting (what superbase/PocketHost provided). ONE Base
//	app PER ORG, opened lazily and pooled, each on its OWN SQLite under
//	{DataDir}/base/{orgSegment}/ — the same "prod = SQLite per tenant" model
//	(HIP-0302) the NewBase leaves (captable/sign/dataroom) use, so an org's
//	collections/records are PHYSICALLY isolated. Served AUTHENTICATED under
//	/v1/base/*, the org resolved from the VALIDATED cloud principal (never a
//	client header). This is the console Bases manager's backend.
//
// The two lanes are deliberately NOT one app: the waitlist is a public, single,
// brand-level instance; hosted Bases are private, per-org, and many.
//
// There was a THIRD prefix, /v1/collections, forwarding to a SEPARATE managed
// Base deployment for the sake of a cross-instance `tenants` registry — one row
// per Base instance, each on its own subdomain. It is gone, and the registry is
// why: it could only ever answer anonymously, because an authenticated request
// is scoped to the caller's org and opens that org's own Base, which has no
// `tenants` collection. So a registry of every org's Bases could not be read by
// anyone who had signed in, and it held zero rows for its whole life. What
// remained was two engines answering one question — an org's collections and
// their records — from two disks, where a record written through one was
// invisible through the other. LANE 2 below is the answer.
//
// MOUNT PREFIX. Base's REST router honours BASE_API_PREFIX (default /v1); this
// package pins it to /v1/base so the per-org engine serves its collections API
// natively at /v1/base/collections/… (self-generated URLs included) and never
// collides with cloud's other /v1 routes. The waitlist plugin binds a FIXED
// /v1/waitlist regardless of the prefix, so the two lanes never overlap.
//
// IAM-NATIVE, ONE AUTH SOURCE. Each per-org app validates bearer tokens against
// Hanzo IAM's JWKS ({IAMIssuer}/v1/iam/.well-known/jwks) as its EXCLUSIVE auth
// source (apis.StoreKey{JWKSURL,ExternalAuthOnly}) — the same IAM the cloud edge
// validates for org routing. The edge selects the org; Base authorises the
// record; both consume ONE IAM. No second auth path is introduced.
//
// FAIL-CLOSED + STAGED. The embed activates only when CLOUD_BASE_EMBED is
// truthy. Absent it, Mount is a no-op except the always-on GET /v1/base/health
// liveness route, so linking this subsystem into every cloud variant changes
// nothing until a single-writer deployment opts in. Activation is one CR env.
//
// WHERE A BASE LIVES (sql.go, files.go): embedded, a Base is two SQLite files in
// the org's directory, single-open + single-writer on one RWO volume. With
// CLOUD_SQL_DSN it is two schemas on hanzo-sql (data and auxiliary, one pair per
// org), moved from the files once under CLOUD_SQL_MOVE, so any pod serves any org.
// With the object store configured (S3_ADMIN_*) its uploaded files are in a bucket
// of the org's. Nothing a Base keeps needs the pod's disk.
package base
