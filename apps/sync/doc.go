// Package sync is data sync: link two endpoints and keep them in step, on a
// webhook, on a schedule, or on demand.
//
// A Sync (/v1/sync) names the two endpoints and the engine reconciles them. Git
// (GitHub/GitLab ⇆ native Hanzo Git) is the one provider registered today; another
// kind is another Provider, with nothing in the engine to change.
//
// Shape (decomplected):
//   - store.go        the sync intent + engine cursor, and the two facts the
//     forge cannot hold: what an advance did, and where a repo replicates to.
//   - engine.go       the ONE place a sync happens: resolve → loop-guard → cursor
//     dedupe → provider.Apply → chain (hop-bounded). Kind-agnostic.
//   - provider.go     the engine↔provider contract (Plan/Apply per kind) + registry.
//   - git_provider.go the git provider, composing the git clients below.
//   - advance.go      the fast-forward-ONLY ref advance — the safety property.
//   - importer.go     those clients, answered against the forge (git.hanzo.ai).
//   - gitexec.go      the hardened `git` subprocess every advance runs through.
//   - sync_api.go     /v1/sync CRUD + /v1/sync/:id/run (manual).
//
// Triggers (GitHub App webhook, Hanzo Git push webhook) resolve to Syncs and call
// cloud.Sync — they never sync directly, so the engine is the single client.
package sync
