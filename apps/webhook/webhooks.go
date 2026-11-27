package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
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

// Mount opens the per-org registry stores, wires /v1/webhook, and starts the bus
// dispatcher (fail-soft). It never returns an error for a bus problem — only for a
// genuinely unusable Deps — so a messaging fault can never abort the binary's boot.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("webhook: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("webhook.Use:  empty cloud.DataDir()")
	}
	b := cloud.NewBase(deps, "webhook")
	stores := cloud.NewOrgStore[*store](b, "webhooks", openStore(cipherOf(deps.MasterKey), deps.KMS))

	st := &state{stores: stores, disp: newDispatcher(stores, b.Log)}
	mounted = st

	svc := &cloud.Service[*state]{Base: b, State: st}
	if err := routes(app, svc); err != nil {
		return err
	}

	// Dispatcher last: registry is already wired, so a bus that is down leaves
	// /v1/webhook fully serving while the consumer retries in the background.
	st.disp.start()

	b.Log.Info("webhooks mounted", "prefix", "/v1/webhook")
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

// routes registers the /v1/webhook CRUD surface as TYPED ops: one registry
// entry each, which is what the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method are all projected from. All eight are typed.
//
// The COLLECTION ROOT is declared on the app with its absolute path, not as the
// group's empty leaf: joinPath normalises an empty leaf to "/", so `g.Get(// "", …)` names /v1/webhook/ — with a trailing slash — and op.Path is the
// identity every projection keys on. That is what the untyped registration did,
// and the published subset carried /v1/webhook/ for a collection every caller
// addresses without the slash. The router is non-strict either way, so this
// moves the artifact and not the wire.
func routes(app cloud.Router, s *cloud.Service[*state]) error {
	o := ops{s: s}
	webhook := app.Group("/v1/webhook")
	webhook.Get("", o.listEndpoints)
	webhook.Post("", o.createEndpoint, zip.WithStatus(http.StatusCreated))
	g := app.Group("/v1/webhook")
	g.Get("/:id", o.getEndpoint)
	g.Put("/:id", o.updateEndpoint)
	g.Delete("/:id", o.deleteEndpoint)
	g.Get("/:id/deliveries", o.listDeliveries)
	g.Post("/:id/test", o.testEndpoint)
	g.Post("/:id/secret", o.rotateSecret)
	return nil
}

// ops binds the per-org stores and the dispatcher to the typed webhook ops. A
// TypedHandler is func(context.Context, *In) (*Out, error) — no parameter for the
// service — so it arrives as a RECEIVER and every op is a method value, which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[*state] }
