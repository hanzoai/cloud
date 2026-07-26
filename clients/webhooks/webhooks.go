// Package webhooks is the platform-global webhook layer (HIP-0106): ONE registry plus
// ONE dispatcher that delivers ANY event on the platform bus to org-registered HTTP
// subscribers. It supersedes commerce's local, billing-scoped delivery path — the
// webhook surface is /v1/webhooks, top-level, never under /v1/billing, and it fans out
// EVERY platform event, not just commerce's.
//
// TWO ORTHOGONAL HALVES.
//
//   - REGISTRY (api.go, store.go) — /v1/webhooks CRUD. An org manages ONLY its own
//     endpoints; each org's registry is a physically separate {DataDir}/orgs/{slug}/
//     webhooks.db (the clients/books per-org SQLite idiom via cloud.OrgStore), so one
//     tenant can never read or mutate another's. The org is the VALIDATED principal
//     (principal.Org, gateway-minted X-Org-Id), exactly like clients/notify — 401 for
//     an unauthenticated caller.
//   - DISPATCHER (dispatch.go, match.go) — a durable JetStream consumer on cloud's own
//     embedded bus (clients/pubsub, stream COMMERCE, subjects commerce.>). It resolves
//     each event's org from the envelope, matches ONLY that org's active subscriptions
//     (NATS subject-wildcard semantics), and POSTs each match with a fresh
//     HMAC-SHA256 signature and a bounded retry ladder. Org isolation is by
//     construction: the store lookup is per-org, so B's endpoint can never receive A's
//     event.
//
// FAIL-SOFT MOUNT. The registry always mounts. The dispatcher is best-effort: no bus
// URL ⇒ inert; a down bus ⇒ background reconnect-retry. A messaging fault never crashes
// the shared cloud binary.
package webhooks

import (
	"context"
	"fmt"

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
// (the same package-global pattern clients/crm and clients/books use).
var mounted *state

// Mount opens the per-org registry stores, wires /v1/webhooks, and starts the bus
// dispatcher (fail-soft). It never returns an error for a bus problem — only for a
// genuinely unusable Deps — so a messaging fault can never abort the binary's boot.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("webhooks.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("webhooks.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("webhooks.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "webhooks")
	stores := cloud.NewOrgStore[*store](deps.DataDir, "webhooks", openStore,
		cloud.WithDurable(b.Durable), cloud.WithStoreLogger(b.Log))

	st := &state{stores: stores, disp: newDispatcher(stores, b.Log)}
	mounted = st

	svc := &cloud.Service[*state]{Base: b, State: st}
	routes(app, svc)

	// Dispatcher last: registry is already wired, so a bus that is down (or absent)
	// leaves /v1/webhooks fully serving while the consumer retries in the background.
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

// routes registers the /v1/webhooks CRUD surface via the express-style group.
func routes(app *zip.App, s *cloud.Service[*state]) {
	g := app.Group("/v1/webhooks")
	g.Get("", cloud.Handle(s, listEndpoints))
	g.Post("", cloud.Handle(s, createEndpoint))
	g.Get("/:id", cloud.Handle(s, getEndpoint))
	g.Put("/:id", cloud.Handle(s, updateEndpoint))
	g.Delete("/:id", cloud.Handle(s, deleteEndpoint))
	g.Get("/:id/deliveries", cloud.Handle(s, listDeliveries))
	g.Post("/:id/test", cloud.Handle(s, testEndpoint))
	g.Post("/:id/rotate-secret", cloud.Handle(s, rotateSecret))
}
