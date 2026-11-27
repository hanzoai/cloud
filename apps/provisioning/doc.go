// Package provisioning is one-click data add-ons: a SQL, key-value, document,
// vector, search or object store, wired straight into your app.
//
// It turns "create a database" into a real logical resource inside an
// already-live, shared product backend, per the unified /v1 binary (HIP-0106).
//
// One HTTP surface, seven kinds, two strategies:
//
// DEDICATED-instance — the four on-demand data add-ons. Each org OWNS its
// instance (an operator Datastore CR in tenant-<org>), so its admin credential
// is naturally tenant-scoped; the assembled DSN is injected as <KIND>_URL into
// the app instance's addons Secret, switching it off Base onto the backend:
//
//	kv        -> Hanzo KV        Datastore type=valkey       kv://…:6379
//	sql       -> Hanzo SQL       Datastore type=postgresql   postgres://…:5432
//	docdb     -> Hanzo DocDB     Datastore type=docdb        mongodb://…:27017
//	datastore -> Hanzo Datastore Datastore type=datastore    datastore://…:8123
//
// SHARED-logical — a logical resource inside an already-live shared backend:
//
//	vector    -> Qdrant      vector.hanzo.svc:6333    PUT /collections/{name}
//	search    -> Meilisearch search.hanzo.svc:7700    POST /indexes
//	s3        -> S3          s3.hanzo.svc:9000        MakeBucket
//
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id /
// c.Org()). Empty org is rejected 403 unless the caller is an admin. The
// physical resource on the shared backend is namespaced "o"<hash(org)>_<name>
// with a FIXED-WIDTH org hash, so the org→name boundary is unambiguous and two
// distinct tenants can never fold onto one backend resource. A global
// UNIQUE(physical_name) guard makes any residual fold fail closed with 409.
//
// Secrets: generated per-resource passwords are sealed in Hanzo KMS
// (client-side encrypted) and only a secret_ref is persisted. When KMS is not
// configured the service degrades safely — it returns the password once in the
// create response and stores NOTHING in plaintext. See kms.go.
package provisioning
