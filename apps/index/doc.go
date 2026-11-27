// Package index is fast full-text search over your own data, typos forgiven.
//
// A native-Go, multi-tenant full-text index on Base/SQLite (or hanzo-sql) that speaks the
// Meilisearch REST dialect.
//
// It is the in-binary replacement for the standalone Meilisearch containers.
// Search is a subsystem of the one cloud binary like every other client, so it
// inherits per-org tenancy, encryption at rest (cek), and the platform's auth
// and o11y instead of running its own process with its own master key and its
// own RWO volume.
//
// # Why the Meilisearch dialect
//
// Hanzo Chat drives search through the `meilisearch@0.38` JS client and its
// mongoMeili Mongoose plugin. Speaking that dialect means chat points MEILI_HOST
// at this surface and needs no client change:
//
//	GET    /v1/index/health                              {"status":"available"}
//	GET    /v1/index/version
//	POST   /v1/index/indexes                             {uid, primaryKey}
//	GET    /v1/index/indexes/:uid
//	GET    /v1/index/indexes/:uid/settings
//	PATCH  /v1/index/indexes/:uid/settings               {filterableAttributes}
//	POST   /v1/index/indexes/:uid/documents              [doc,…]  add/replace
//	PUT    /v1/index/indexes/:uid/documents              [doc,…]  update/upsert
//	GET    /v1/index/indexes/:uid/documents              ?limit&offset
//	GET    /v1/index/indexes/:uid/documents/:id
//	DELETE /v1/index/indexes/:uid/documents/:id
//	POST   /v1/index/indexes/:uid/documents/delete-batch [id,…]
//	POST   /v1/index/indexes/:uid/search                 {q, filter, limit, offset}
//	GET    /v1/index/tasks/:uid
//
// Error bodies use Meilisearch's {message, code, type, link} shape rather than
// cloud's, because the JS client branches on those codes — index_not_found is
// how mongoMeili decides to create an index.
//
// # Tenancy
//
// A standalone Meilisearch has one global keyspace guarded by a master key, so
// every consumer sharing an instance shares its indexes. Here the tenant is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026), never a client-supplied header — and every query
// filters WHERE org=?. Two orgs may both hold an index named "messages" without
// ever seeing each other's documents. The bearer token is the org's cloud API
// key; the JS client already sends `Authorization: Bearer …`, so the wire shape
// is unchanged.
//
// Within an org, chat scopes results to the end user with a `user = "<id>"`
// filter, which this surface honours as Meilisearch does.
//
// # Writes are synchronous
//
// Meilisearch queues writes and returns an EnqueuedTask. SQLite applies them
// before the response, so the task ids reported here are already complete and
// GET /tasks/:uid always reports `succeeded` — a client polling waitForTask
// resolves immediately rather than never.
package index
