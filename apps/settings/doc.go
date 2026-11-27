// Package settings is how an org configures each product it uses, secret fields
// included.
//
// It is the per-org, per-product configuration plane for the unified Hanzo Cloud
// binary: the /v1/settings/:product surface behind every product's detail view in
// console.hanzo.ai (#59). Reads and writes are backed by a durable per-tenant SQLite
// store with KMS custody for any secret-typed field.
//
// ONE settings engine, EVERY product. The console drives all products' Settings tab
// through this single surface (product id → :product). There is no per-product
// bespoke server code — a product is just an (org, product) key.
//
// Surface (all org-scoped; /v1 only):
//
//	GET /v1/settings/:product   org config (read, secrets masked)  -> View
//	PUT /v1/settings/:product   org config (write)                 -> View
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request. The org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner (HIP-0026) — and is NEVER read from a query param, body, or client header.
// It is the mandatory predicate on every store statement. The client chooses a
// PRODUCT (validated against a slug shape); it never supplies the org.
//
// SECRET CUSTODY. A secret field's VALUE lives ONLY in KMS at
// orgs/{org}/settings/{product}/{key}; the store keeps only the non-secret JSON plus
// the list of secret key NAMES (so the read path knows which fields are set-but-
// masked). A plaintext secret can never reach SQLite — a secret write routes to KMS
// or fails closed (503).
//
// NOT OBSERVABILITY. This surface once shared a package with the o11y read paths;
// those reads live in apps/o11y now. A product's config and a product's telemetry
// are different questions with different stores, so they are different planes.
package settings
