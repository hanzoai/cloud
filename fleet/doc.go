// Package fleet is your own compute, attached: bring a Kubernetes cluster, a GPU
// box or bare metal and run work on it.
//
// It is the ONE per-org registry of that attached compute (BYO k8s clusters /
// BYO GPU / bare metal), the single source of truth consumed by BOTH the fleet
// surface (apps/visor, which serves /v1/compute/clusters — managed clusters from Visor
// MERGED with these BYO ones) AND ML serving (apps/ml, whose dynForOrg federates a
// workload onto the org's registered cluster). One registry, two consumers — never a
// second cluster surface.
//
// Tenant isolation is the org boundary, narrowed by the org SUB-SCOPE (project):
// the kubeconfig is sealed in the org's KMS (MPC nodes see only ciphertext) under a
// per-org(+project) ref prefix, and every method takes the org + project as resolved
// from the ZAP-propagated, gateway-validated X-Org-Id / X-Project-Id — never a client
// field. So lux sees only lux's clusters, zoo only zoo's, a customer only their own —
// and, within an org, one project's fleet is a distinct shard. The DEFAULT project
// (principal.IsDefaultProject) keeps the legacy org-only key, so existing single-
// project fleets are untouched (scopeRef).
package fleet
