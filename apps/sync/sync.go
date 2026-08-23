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

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// state is the subsystem's mounted state: the per-org syncs store cache, and the
// forge credential this process holds.
type state struct {
	stores *cloud.OrgStore[*store]
	// forge resolves the deployment's forge client, re-reading the machine
	// credential from KMS on its own schedule so a rotation is live without a
	// restart. It is per-PROCESS state, not per-request: a client is an HTTP
	// client and a token, and the thing worth not repeating is the KMS read.
	forge *forge.Source
}

// mounted is the active service, read by the reconcile func (registered as the
// cloud.SyncFunc, reached from webhook goroutines) and written once at Mount/
// Shutdown — an atomic.Pointer so a detached run reads it race-free (nil ⇒ unmounted).
var mounted atomic.Pointer[cloud.Service[state]]

// schedStop stops the periodic reconcile scheduler (scheduler.go). Set once at Mount,
// called once at Shutdown — both lifecycle-serialized, never concurrent. Idempotent
// (startScheduler's closure self-guards), nil-safe.
var schedStop func()

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single door a validated org walks through —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
//
// sync is org-scoped, not project-scoped — a link binds two endpoints within
// one org.
func storeFor(s *cloud.Service[state], org string) (*store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires /v1/sync, registers the git provider, and installs the reconcile func
// as the cloud.SyncFunc so triggers (cloud.Sync) reach it.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sync.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("sync.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "sync")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "sync", openStore),
		forge:  &forge.Source{},
	}}
	mounted.Store(s)

	if err := routes(app, s); err != nil {
		return err
	}
	registerProvider(gitProvider{})
	cloud.RegisterSync(reconcileEvent)
	// The git object clients, answered against the FORGE (importer.go). This app
	// owns them now because it owns the one thing the forge cannot hold — the
	// record of what an advance did — and because the forge itself is reachable
	// from any process, so there is no store to be co-resident with any more.
	cloud.RegisterGitImporter(importer{})
	cloud.RegisterGitMirrorController(mirrorControl{})
	// The same reconcile and the same git clients, offered to the processes the
	// triggers actually land in — integrations and the webhook door, neither of
	// which is this one (run_plane.go, import_plane.go).
	exposeRun()
	exposeImport()

	schedStop = startScheduler(s) // freshness: periodic reconcile of every poll sync (env-gated)

	b.Log.Info("sync mounted", "brand", deps.Brand, "providers", "git")
	return nil
}

// Shutdown stops the reconcile scheduler (waiting for an in-flight sweep to drain) and
// then closes every open per-org store — in THAT order, so a store is never closed out
// from under a running reconcile. Idempotent.
//
// The git clients are withdrawn first, so a call arriving mid-shutdown gets the
// fail-closed "not registered" rather than reaching a store that is about to
// close under it.
func Shutdown() error {
	cloud.RegisterGitImporter(nil)
	cloud.RegisterGitMirrorController(nil)
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

// terminal adapts cloud.Terminal to zip's leaf-wrapping Middleware so a TYPED op
// gets the same in-band error write an untyped handler got from
// cloud.Terminal(cloud.Handle(...)). ONE implementation of the behaviour, reached two
// ways — this is a signature adapter, not a second copy: zip.Handler is a defined
// type, so `func(Handler) Handler` and cloud.Terminal's `func(func(*Ctx) error)
// func(*Ctx) error` are not the same type even though each value converts.
func terminal(next zip.Handler) zip.Handler { return cloud.Terminal(next) }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the /v1/sync control plane: the record and the service share the
// one word "sync". Org-scoped like every tenant surface (the gateway-validated
// principal selects the org).
//
// Every op is wrapped in cloud.Terminal: sync mounts AFTER the commerce embed, whose
// /v1 ErrorHandlerJSON filter flattens any PROPAGATED handler error to HTTP 500.
// Terminal writes the reject status (401 no-principal, 400 bad body, 404 not-found)
// in-band, so the filter has nothing to flatten and the real 4xx stands. With(...)
// carries that wrapper down to a typed op exactly as it wraps an untyped leaf, so
// going typed did not cost this surface its real statuses.
//
// The group is "/v1" and each op names its own "/sync…" leaf, because the collection
// endpoints sit AT /v1/sync and a group prefix composed with an empty leaf yields
// "/v1/sync/" — a different address. Composed this way every op's published path is
// exactly the path the router matches.
//
// A typed op receives a context.Context and its decoded In and nothing else, so
// the validated org crosses on the context. cloud.Bridge parks it there, and the
// composer owns that install — the fused host at its root, a plugin program in
// its constructor — so this package installs nothing and only reads it.
func routes(app cloud.Router, s *cloud.Service[state]) error {
	za := cloud.ZipApp(app)
	if za == nil {
		return fmt.Errorf("sync.Mount: router exposes no op registry")
	}
	g := za.With(terminal).Group("/v1")
	o := syncOps{s: s}
	zip.Post(g, "/sync", o.create)
	zip.Get(g, "/sync", o.list)
	zip.Get(g, "/sync/:id", o.get)
	zip.Patch(g, "/sync/:id", o.patch)
	zip.Delete(g, "/sync/:id", o.delete)
	// Manual run: reconcile one sync now (initial import, or a re-sync after an
	// upstream you couldn't webhook). A distinct trailing segment, never shadows :id.
	zip.Post(g, "/sync/:id/run", o.run, zip.WithStatus(http.StatusAccepted))
	return nil
}
