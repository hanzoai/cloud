// Package webhooks is how your app hears about events: register an endpoint,
// pick the events, get each one delivered and signed.
//
// It is the platform-global webhook layer (HIP-0106) — ONE registry plus ONE
// dispatcher that delivers ANY event on the platform bus to org-registered HTTP
// subscribers. It supersedes commerce's local, billing-scoped delivery path — the
// webhook surface is /v1/webhooks, top-level, never under /v1/billing, and it fans out
// EVERY platform event, not just commerce's.
//
// TWO ORTHOGONAL HALVES.
//
//   - REGISTRY (api.go, store.go) — /v1/webhooks CRUD. An org manages ONLY its own
//     endpoints; each org's registry is a physically separate {DataDir}/orgs/{slug}/
//     webhooks.db (the apps/books per-org SQLite idiom via cloud.OrgStore), so one
//     tenant can never read or mutate another's. The org is the VALIDATED principal
//     (principal.Org, gateway-minted X-Org-Id), exactly like apps/notify — 401 for
//     an unauthenticated caller.
//   - DISPATCHER (dispatch.go, match.go) — a durable JetStream consumer on the platform
//     bus (apps/pubsub), over the streams dispatch.go names: COMMERCE (commerce.>, owned
//     by hanzoai/commerce) and the event plane (event.>, owned by apps/analytics, which
//     publishes it). It resolves each event's org from the envelope, matches ONLY that
//     org's active subscriptions (NATS subject-wildcard semantics), and POSTs each match
//     with a fresh HMAC-SHA256 signature and a bounded retry ladder. Org isolation is by
//     construction: the store lookup is per-org, so B's endpoint can never receive A's
//     event.
//
// CONSUMER ONLY. This package publishes nothing and owns no stream. It once held the
// publish half of the event plane too — its own stream name, its own envelope — which
// made two subsystems owners of one subject space; JetStream answers that with
// "subjects overlap with an existing stream", so the second owner to arrive delivers
// nothing, on every stream, forever. The producer lives with the data now
// (analytics.PublishEvents).
//
// FAIL-SOFT MOUNT. The registry always mounts. The dispatcher is best-effort: a down bus
// ⇒ background reconnect-retry. A messaging fault never crashes the process.
package webhooks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// state is the subsystem's own data: the per-org registry stores (shared by the CRUD
// handlers AND the dispatcher, so a just-created endpoint is instantly visible to
// delivery) and the dispatcher. Shared deps (logger, brand) live in the embedded
// cloud.Base, reached from a handler as s.Log.
type state struct {
	stores *cloud.OrgStore[*store]
	disp   *dispatcher
}

// mounted is the process-wide handle Shutdown reaches the dispatcher + stores through
// (the same package-global pattern apps/crm and apps/books use).
var mounted *state

// Mount opens the per-org registry stores, wires /v1/webhooks, and starts the bus
// dispatcher (fail-soft). It never returns an error for a bus problem — only for a
// genuinely unusable Deps — so a messaging fault can never abort the binary's boot.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("webhooks.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("webhooks.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("webhooks.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "webhooks")
	stores := cloud.NewOrgStore[*store](b, "webhooks", openStore)

	st := &state{stores: stores, disp: newDispatcher(stores, b.Log)}
	mounted = st

	svc := &cloud.Service[*state]{Base: b, State: st}
	if err := routes(app, svc); err != nil {
		return err
	}

	// Dispatcher last: registry is already wired, so a bus that is down leaves
	// /v1/webhooks fully serving while the consumer retries in the background.
	st.disp.start()

	b.Log.Info("webhooks mounted", "prefix", "/v1/webhooks")
	return nil
}

// Shutdown stops the dispatcher (draining its workers) and closes every open per-org
// store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	if mounted.disp != nil {
		mounted.disp.stop()
	}
	if mounted.stores != nil {
		return mounted.stores.CloseAll()
	}
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/webhooks openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the /v1/webhooks CRUD surface as TYPED ops: one registry
// entry each, which is what the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method are all projected from. All eight are typed.
//
// The COLLECTION ROOT is declared on the app with its absolute path, not as the
// group's empty leaf: joinPath normalises an empty leaf to "/", so `zip.Get(g,
// "", …)` names /v1/webhooks/ — with a trailing slash — and op.Path is the
// identity every projection keys on. That is what the untyped registration did,
// and the published subset carried /v1/webhooks/ for a collection every caller
// addresses without the slash. The router is non-strict either way, so this
// moves the artifact and not the wire.
func routes(app cloud.Router, s *cloud.Service[*state]) error {
	// The typed registrars take the App behind the Router: a typed op is a route
	// PLUS a registry entry, and the registry lives on the App. A subsystem that
	// cannot reach it must fail its mount rather than serve routes no projection
	// knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("webhooks.Mount: router exposes no zip.App, so no typed op could be registered")
	}
	g := app.Group("/v1/webhooks")
	// The Bridge FIRST, bounded to the subtree webhooks owns: a typed op receives
	// only a context, so the validated org has to be parked there, and fiber runs
	// middleware in registration order — one installed after these leaves would
	// never run. cloud.Serve installs one app-wide too; nesting is harmless (the
	// inner one is what the handler sees), and having it here is what makes this
	// package's own tests — which mount on a bare app — exercise the same tenancy
	// the binary does.
	g.Use(cloud.Bridge())

	o := ops{s: s}
	zip.Get(zapp, "/v1/webhooks", o.listEndpoints)
	zip.Post(zapp, "/v1/webhooks", o.createEndpoint, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/:id", o.getEndpoint)
	zip.Put(g, "/:id", o.updateEndpoint)
	zip.Delete(g, "/:id", o.deleteEndpoint)
	zip.Get(g, "/:id/deliveries", o.listDeliveries)
	zip.Post(g, "/:id/test", o.testEndpoint)
	zip.Post(g, "/:id/secret", o.rotateSecret)
	return nil
}

// ops binds the per-org stores and the dispatcher to the typed webhook ops. A
// TypedHandler is func(context.Context, *In) (*Out, error) — no parameter for the
// service — so it arrives as a RECEIVER and every op is a method value, which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[*state] }
