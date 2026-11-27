// Package dataroom is a secure document room you share by link and watch page by
// page.
//
// It folds hanzoai/dataroom (a Papermark fork: Next.js + Prisma + Postgres,
// "open-source DocSend/dataroom") FULLY into the unified hanzoai/cloud binary
// as an in-process subsystem (HIP-0106, task #101 / epic #96). Cloud serves the
// dataroom surface (/v1/dataroom/*) ITSELF — no standalone dataroom pod, no
// Postgres, no Next.js.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, on the SHARED binding. The dataroom
// business logic (documents, data rooms, shareable links with access controls,
// viewers, per-page view analytics) is a self-contained goja bundle — the ESM-free
// port of the Papermark API handlers, taken as a PINNED module
// (github.com/hanzoai/dataroom, checksummed in go.sum) rather than copied in, the
// same way the sign and captable folds take theirs. It runs in-process on the REUSABLE
// clients/goja host — the SAME RW-Base binding captable (#97) pilots and esign
// (#100) reuses — which injects __db/__newId/__now and one SQLite file per tenant,
// one transaction per request. This leaf adds only: the per-tenant Schema, the
// object-storage client for document bytes, a bcrypt HostFn for link passwords, and
// the public link→org index. Zero domain logic lives in Go.
//
//	dataroom bundle (pinned module)  +  per-tenant Schema  +  __bcrypt HostFn
//	                    │
//	             clients/goja.NewBase(...)   ← __db/__newId/__now, per-tenant Base,
//	                    │                       one transaction per request
//	             /v1/dataroom/* zip routes
//
// STORAGE. Document BYTES never touch the bundle or local disk: the leaf stores
// them through the cloud object-storage client (deps.VFS — the S3 data
// plane) keyed by an org-scoped opaque key (a Go storage host-fn over the s3/storage
// subsystem), and the bundle persists only that key via __db. View-analytics events
// (page-by-page tracking) are Base rows in the tenant DB.
//
// AUTH. Admin routes — every read and every write of documents, rooms, links,
// analytics and the trust desk — require a validated cloud principal (principal.Org
// → org) that the platform's Admin scope admits (cloud.Admin: a SuperAdmin, or an
// admin of that org). A member who is neither is refused 403: belonging to an org
// does not open its data room. Every write is also refused when a page carried the
// caller's cookie to it (cloud.CSRF). Public viewer routes carry no principal and
// resolve their org from the link index (a link id → org routing table — the one
// cross-tenant provider), and the published trust centre is read by anyone. Tenant
// isolation is the per-org SQLite file NewBase selects from that org.
//
// ACTIVATION: dataroom is NOT staged — it mounts under the mount-all default
// (empty CLOUD_ENABLE), so the one binary serves /v1/dataroom/* from first boot.
// There is no standalone dataroom pod to defer to (the Papermark/Next.js/Postgres
// app is retired by this fold — no such deployment runs in the fleet), so cloud's
// fresh per-tenant Base/SQLite is authoritative from the first write, with no data
// to migrate.
package dataroom
