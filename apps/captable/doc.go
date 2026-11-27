// Package captable is your cap table: stakeholders, share classes, grants, SAFEs,
// rounds, and who owns what.
//
// In full: stakeholders, share classes, share certificates and transfers, option
// grants and equity plans, SAFEs and convertible notes, priced rounds and their
// investments, and the summary that totals outstanding and fully-diluted ownership
// from them. It runs per tenant in the unified cloud binary (HIP-0106), on a SQLite
// file or, with CLOUD_SQL_DSN set, on a schema of hanzo-sql.
//
// WRAP, DON'T REWRITE — the read-WRITE variant. Where apps/plan + apps/pricing
// host a read-only @hanzo catalog in goja, captable hosts the tRPC
// business LOGIC (ported to a self-contained goja bundle in github.com/hanzoai/
// captable) and gives it PERSISTENCE over per-tenant Base/SQLite. The bundle
// carries logic; the Go host carries storage. The client between them is the
// REUSABLE apps/goja binding (the RW-Base goja host), which esign (#100)
// and dataroom (#101) reuse unchanged — this leaf is just:
//
//	captable bundle (github.com/hanzoai/captable.Bundle)  +  the per-tenant Schema
//	                         │
//	                  apps/goja.NewBase(...)   ← injects __db/__newId/__now,
//	                         │                       one SQLite file per tenant,
//	                  /v1/captable/* zip routes     one transaction per request
//
// No Prisma, no Postgres, no Next.js in this path. Every route resolves the org
// from the VALIDATED cloud principal (principal.Org), never a client header,
// and that org selects the tenant's DB file AND scopes every row. Every route —
// read or write — also requires the platform's Admin scope (cloud.Admin: a
// SuperAdmin, or an admin of that org): a cap table is read and kept by the people
// who run the company, and a member who is neither is refused 403. Every write is
// also refused when a page carried the caller's cookie to it (cloud.CSRF).
//
// ACTIVATION: captable is NOT staged — it mounts under the mount-all default
// (empty CLOUD_ENABLE), so the one binary serves /v1/captable/* from first boot.
// There is no standalone Captable,Inc pod to defer to (the Next.js/Prisma/Postgres
// app is retired by this fold — no such deployment runs in the fleet), so cloud's
// fresh per-tenant Base/SQLite is authoritative from the first write, with no data
// to migrate.
package captable
