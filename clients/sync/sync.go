// Package sync is the universal sync service (/v1/sync): cloud↔cloud data
// sync between connected platforms, expressed as Syncs the engine runs. Git
// (GitHub/GitLab ⇆ native Hanzo Git) is the FIRST provider; storage, db, and other
// kinds are new providers at their own kind with nothing in the engine to change.
//
// Shape (decomplected):
//   - store.go        ONE table, syncs — the sync intent + engine cursor state.
//   - engine.go       the ONE place a sync happens: resolve → loop-guard → cursor
//     dedupe → provider.Apply → chain (hop-bounded). Kind-agnostic.
//   - provider.go     the engine↔provider contract (Plan/Apply per kind) + registry.
//   - git_provider.go the git provider, composing the existing git object-plane seams.
//   - sync_api.go     /v1/sync CRUD + /v1/sync/:id/run (manual).
//
// Triggers (GitHub App webhook, Gitea push webhook) resolve to Syncs and call
// cloud.Sync — they never sync directly, so the engine is the single seam.
package sync

import (
	"fmt"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// state is the subsystem's mounted state: the per-org syncs store cache.
type state struct {
	stores *cloud.OrgStore[*store]
}

// mounted is the active service, read by the reconcile func (registered as the
// cloud.SyncFunc, reached from webhook goroutines) and written once at Mount/
// Shutdown — an atomic.Pointer so a detached run reads it race-free (nil ⇒ unmounted).
var mounted atomic.Pointer[cloud.Service[state]]

// schedStop stops the periodic reconcile scheduler (scheduler.go). Set once at Mount,
// called once at Shutdown — both lifecycle-serialized, never concurrent. Idempotent
// (startScheduler's closure self-guards), nil-safe.
var schedStop func()

// storeFor resolves the caller's org-scoped syncs store (one SQLite file at
// {DataDir}/orgs/{org}/sync.db). Sync is org-scoped, not project-scoped — a link
// binds two endpoints within one org.
func storeFor(s *cloud.Service[state], org string) (*store, error) {
	return s.State.stores.For(org, "")
}

// Mount wires /v1/sync, registers the git provider, and installs the reconcile func
// as the cloud.SyncFunc so triggers (cloud.Sync) reach it.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sync.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("sync.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("sync.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "sync")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(deps.DataDir, "sync", openStore),
	}}
	mounted.Store(s)

	routes(app, s)
	registerProvider(gitProvider{})
	cloud.RegisterSync(reconcileEvent)
	schedStop = startScheduler(s) // freshness: periodic reconcile of every poll sync (env-gated)

	b.Log.Info("sync mounted", "brand", deps.Brand, "providers", "git")
	return nil
}

// Shutdown stops the reconcile scheduler (waiting for an in-flight sweep to drain) and
// then closes every open per-org store — in THAT order, so a store is never closed out
// from under a running reconcile. Idempotent.
func Shutdown() error {
	if schedStop != nil {
		schedStop()
		schedStop = nil
	}
	s := mounted.Load()
	if s == nil {
		return nil
	}
	err := s.State.stores.CloseAll()
	mounted.Store(nil)
	return err
}

// routes registers the /v1/sync control plane: the record and the service share the
// one word "sync". Org-scoped like every tenant surface (the gateway-validated
// principal selects the org).
//
// Every handler is wrapped in cloud.Terminal: sync mounts AFTER the commerce embed,
// whose /v1 ErrorHandlerJSON filter flattens any PROPAGATED handler error to HTTP
// 500. Terminal writes the reject status (401 no-principal, 400 bad body, 404
// not-found) in-band, so the filter has nothing to flatten and the real 4xx stands.
func routes(app *zip.App, s *cloud.Service[state]) {
	// Collection endpoints sit AT the group root (/v1/sync). Group(p).Method("")
	// yields "p/", so these stay flat on app to preserve the exact path.
	app.Post("/v1/sync", cloud.Terminal(cloud.Handle(s, createSync)))
	app.Get("/v1/sync", cloud.Terminal(cloud.Handle(s, listSyncs)))
	g := app.Group("/v1/sync")
	g.Get("/:id", cloud.Terminal(cloud.Handle(s, getSync)))
	g.Patch("/:id", cloud.Terminal(cloud.Handle(s, patchSync)))
	g.Delete("/:id", cloud.Terminal(cloud.Handle(s, deleteSync)))
	// Manual run: reconcile one sync now (initial import, or a re-sync after an
	// upstream you couldn't webhook). A distinct trailing segment, never shadows :id.
	g.Post("/:id/run", cloud.Terminal(cloud.Handle(s, runSync)))
}
