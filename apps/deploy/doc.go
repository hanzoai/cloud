// Package deploy is Hanzo CD: see what each app is running, sync it, and roll
// back a bad release.
//
// It is the GitOps plane at /v1/deploy — observe the operator-managed fleet
// (applications, resource tree, per-object health, live diff, logs), and
// reconcile it (sync, git → cluster with prune-safe self-heal).
//
// Each operator hanzo.ai/v1 App CR IS a GitOps Application: the desired state
// declared for one workload, which the Hanzo operator reconciles into a
// Deployment + Service + Ingress (+ HPA/PDB/Pods). This plane OBSERVES that
// reconciliation the way a CD controller observes a synced Application —
//
// and it does so at the ArgoCD dashboard's own addresses, because the console that
// consumes it is that dashboard (dashboard.go):
//
//	GET  /v1/deploy/applications                    — the fleet list: name, declared
//	                                                  version, health, sync, per app.
//	GET  /v1/deploy/applications/{name}             — one app, with its resource list.
//	GET  /v1/deploy/applications/{name}/resource-tree — the owned-resource tree
//	                                                  (ownerRef edges) with per-node
//	                                                  health + sync.
//	POST /v1/deploy/applications/{name}/sync        — sync the application now (SuperAdmin).
//	GET  /v1/deploy/gitops                          — the CD plane's own Applications.
//	GET  /v1/deploy/live                            — what each service runs: images,
//	                                                  build commit, applied commit.
//	POST /v1/deploy/reconcile                       — render the configured git source
//	                                                  and apply it, once (engine_mount.go).
//
// SECURITY — the projection READS are TENANT-SCOPED and the WRITES stay SuperAdmin-
// only, all fail-closed on the SAME identity boundary the rest of cloud trusts
// (resolveScope, scope.go — validated principal + injective namespace.Sanitize
// + the principal.Super SuperAdmin predicate): a SuperAdmin sees/mutates the whole fleet,
// a validated org member sees ONLY its own org's apps (hanzo.ai/org label), and the
// sync write remains SuperAdmin-only. Secret objects are never
// surfaced (no node, no manifest) so the tree can never leak materialized env. The
// user-facing per-org PaaS is /v1/platform; this is the platform-operator console the
// admin dashboard consumes, now also serving a read-only per-org reflection.
//
// GitOps note (the follow-on client): today the CR is the desired-state source and a
// rollback/rollout PATCHES it directly (P1's RegisterServiceReleaser), so deploys
// work now. The end-state is true GitOps on OUR native git — RegisterPushBuilder
// commits the CR image-tag change to the manifest repo on git.hanzo.ai
// (github.com/hanzoai/git) and this engine syncs that repo → cluster with
// self-heal. The desired-vs-live diff below is already structured for that: it
// reads a desired source that is "cluster last-applied" now and becomes the
// git.hanzo.ai manifest later, with no shape change.
package deploy
