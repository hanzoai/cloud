// Package ml is model serving: deploy a model behind an endpoint and call it.
//
// The /v1/ml/* routes are a thin, tenant-scoped bridge that turns ONE kserve
// CustomResource into a small REST API. No ML logic is reimplemented here —
// kserve owns reconciliation; this subsystem only translates REST <-> the
// Kubernetes API and enforces tenant isolation.
//
// One resource, one CRUD shape (kserve names are internal/opaque — the
// user-facing model catalog lives in the hub, never here, so no upstream model
// identity is ever introduced by this layer):
//
//	/v1/ml/models   InferenceService   serving.kserve.io/v1beta1
//
// Plus one leaf surface: POST /v1/ml/models/{name}/predict proxies the request
// body to the model's kserve v2 data plane (/v2/models/{name}/infer at the
// InferenceService's cluster-internal address).
//
// TRAINING IS NOT HERE: it is /v1/train (apps/train, HIP-1333). Per-org
// model-shape SEARCH is /v1/risk/search, which runs natively in the org's own
// sandbox. One endpoint each.
//
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id / c.Org())
// narrowed by the org SUB-SCOPE (X-Project-Id / principal.Project), and lands in a
// PER-ORG(+PROJECT) Kubernetes namespace: "ml-"<org> for the default project (the
// backward-compatible single-project shape) and "ml-"<org>"-"<project> for a
// non-default one. The namespace IS the tenant boundary — a tenant physically cannot
// name into, list, read, mutate or predict against another org's (or project's)
// resources because the dynamic client is always pinned to the caller's namespace.
// Both org and project are validated against strict DNS-label regexes (no lossy
// sanitize), so the (org, project)->namespace map is injective: two distinct scopes
// can never fold onto one namespace. Empty org is rejected 403 unless the caller is a
// gateway-minted admin (bucketed under the literal "ml-admin" namespace).
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev. It is NOT
// hung off the shared cloud.Deps: a raw Kubernetes client has none of the
// in-process/ZAP-RPC duality the Deps inter-subsystem clients model, and it is
// used by exactly this one subsystem — so it stays self-contained here, the
// same way provisioning builds its own backend clients. When no kubeconfig
// is resolvable the subsystem mounts anyway and every endpoint fails closed:
// mutating routes return 503 and the health routes report status "degraded"
// with the real init error (never a fake success).
package ml
