package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
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

// backfillStop stops the boot backfill of linked projects (project.go) and waits
// for it. Set once at Mount, called once at Shutdown; nil-safe.
var backfillStop func()

// storeFor is the ONE way this package reaches a store: it names the database
// through orgns.OrgNamespace — the single path a validated org takes —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
//
// sync is org-scoped, not project-scoped — a link binds two endpoints within
// one org.
func storeFor(s *cloud.Service[state], org string) (*store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires /v1/sync, registers the git provider, and installs the reconcile func
// as the cloud.SyncFunc so triggers (cloud.Sync) reach it.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("sync: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("sync.Use:  empty DataDir")
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
	// triggers actually land in — integrations and the webhook receiver, neither of
	// which is this one (run_plane.go, import_plane.go).
	exposeRun()
	exposeImport()

	schedStop = startScheduler(s) // freshness: periodic reconcile of every poll sync (env-gated)

	// Every repository link is a project; the ones that are not yet are made so,
	// off-thread, once the projects app is up (project.go).
	bctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		backfill(s, bctx)
	}()
	backfillStop = func() {
		cancel()
		<-done
	}

	b.Log.Info("sync mounted", "brand", cloud.Brand(), "providers", "git")
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
	if backfillStop != nil {
		backfillStop()
		backfillStop = nil
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
// A typed op receives a context.Context and its decoded In and nothing else, so
// the validated org crosses on the context. cloud.Bridge parks it there, and the
// composer owns that install — the fused host at its root, a plugin program in
// its constructor — so this package installs nothing and only reads it.
func routes(app cloud.Router, s *cloud.Service[state]) error {
	if app == nil {
		return fmt.Errorf("sync.Use:  router exposes no op registry")
	}
	g := app.With(terminal).Group("/v1/sync")
	o := syncOps{s: s}
	g.Post("", o.create)
	g.Get("", o.list)
	g.Get("/:id", o.get)
	g.Patch("/:id", o.patch)
	g.Delete("/:id", o.delete)
	// Manual run: reconcile one sync now (initial import, or a re-sync after an
	// upstream you couldn't webhook). A distinct trailing segment, never shadows :id.
	g.Post("/:id/run", o.run, zip.WithStatus(http.StatusAccepted))
	return nil
}
