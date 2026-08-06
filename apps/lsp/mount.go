package lsp

// mount.go is this subsystem's registration: build the state, bind the doors.
//
// It follows apps/code's Mount exactly — same signature, same fail-closed
// argument checks, and routes() as a FUNCTION rather than inline so this
// package's tests drive the REAL registration instead of a reconstruction of it
// that can drift from what the binary serves.
//
// A NOTE ON MOUNT ORDER. manifest/apps.go is the hand-authored fleet list whose
// SLICE POSITION is the order (build.go: "There is NO Order field"). lsp's row
// sits immediately after code's, and manifest/order_test.go freezes that
// sequence, so a reorder stays a decision. Routing does not depend on it:
// /v1/code/lsp is a deeper static prefix than both /v1/code and ai's bare /v1,
// and nested static prefixes resolve by SPECIFICITY.
//
// There is no Shutdown. The subprocesses and the checkouts this app used to own
// are the daemon's now; what is left here is an http.Client, which the process
// exiting reclaims. A hook that closed nothing would be a hook nobody could
// delete later without proving it closed nothing.

import (
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// state is the subsystem: the shared Base plus the one daemon client. It holds
// no org in a field — the org is a parameter on every call, so one process serves
// all orgs and an org can never be captured from stale state.
type state struct {
	cloud.Base
	daemon *daemon
}

// Mount wires /v1/code/lsp onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("lsp.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("lsp.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("lsp.Mount: empty DataDir")
	}
	s := &state{Base: cloud.NewBase(deps, "lsp"), daemon: newDaemon()}

	if err := routes(app, s); err != nil {
		return err
	}

	// The key is a KMS secret, so what is logged is whether one is PRESENT. A
	// deployment that forgot it otherwise looks healthy until the first query
	// answers 503.
	s.Log.Info("lsp surface mounted (proxy)",
		"brand", deps.Brand, "upstream", s.daemon.url, "keyed", s.daemon.key != "")
	return nil
}

// routes registers the /v1/code/lsp surface: one typed op per question.
//
// Typed rather than raw so the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method are all projected from these five entries — a
// surface built for coding agents, where the MCP tool list is not a side benefit
// of typing it but the point. Five doors rather than one door with an `op` field
// for the same reason: an agent picks a tool by its name and its description, and
// a union behind one name is a tool it has to be told how to use.
func routes(app cloud.Router, s *state) error {
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	g := app.Group("/v1/code/lsp")
	zip.Post(g, "/hover", s.hover)
	zip.Post(g, "/locate", s.locate)
	zip.Post(g, "/symbols", s.symbols)
	zip.Post(g, "/diagnostics", s.diagnostics)
	zip.Post(g, "/complete", s.complete)
	return nil
}
