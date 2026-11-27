// Package sbom is what is inside a container image: every component, resolvable by
// digest or image ref.
//
// CI posts a CycloneDX software bill of materials keyed by image digest, and
// /v1/sbom resolves that component set back.
//
// GLOBAL BY DESIGN. Unlike the analytics lens (which is strictly per-org), an SBOM
// belongs to an image DIGEST, not a tenant — the digest is content-addressed, so
// any tenant deploying that image resolves the SAME component set. The store is
// therefore cross-tenant on purpose: ingest is gated to super-admin/CI (the build
// fleet), and resolve exposes only the immutable bill-of-materials of an image, no
// tenant data. This is why there is no org predicate here.
//
// ONE datastore client. Like clients/analytics, this package rides the SAME
// datastore-go client the ai subsystem opens in the shared Bootstrap
// (ai/object.DatastoreExec/DatastoreQuery). It never opens a second connection.
//
// Surface (/v1 only):
//
//	POST /v1/sbom          ingest a CycloneDX SBOM (super-admin / CI only)
//	GET  /v1/sbom/{ref}    resolve by image digest OR image ref (for the console)
//	GET  /v1/sbom/health   liveness + datastore connectivity (not JWT-gated)
//
// Registered as id "sbom" with cloud.HealthOwner + order 137: it serves its own
// /v1/sbom/health, so serve.go skips the generic liveness route. Order 137 binds
// /v1/sbom/* before the ai subsystem's /v1/* catch-all (150).
package sbom
