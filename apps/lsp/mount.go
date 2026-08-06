package lsp

// mount.go is this subsystem's registration: build the state, bind the door.
//
// It follows apps/code's Mount exactly — same signature, same fail-closed
// argument checks, same package-global for Shutdown to reach, and routes() as a
// FUNCTION rather than inline so this package's tests drive the REAL registration
// instead of a reconstruction of it that can drift from what the binary serves.
//
// A NOTE ON MOUNT ORDER. The brief asked for "order ~135, before ai's /v1/*
// catch-all at 150". Those integers no longer exist: apps.Wire() is gone, and
// manifest/apps.go is the hand-authored fleet list whose SLICE POSITION is the
// order (build.go: "There is NO Order field"). The "Order 134" in code.go's
// header is a comment describing a position, not a field. So lsp's row sits
// immediately after code's in manifest.Apps — the same intent expressed in the
// mechanism that actually exists — and manifest/order_test.go's frozen sequence
// is updated in the same commit, which is how a reorder stays a decision.
// Routing does not in fact depend on it: nested static prefixes resolve by
// SPECIFICITY, so /v1/lsp beats ai's /v1 wherever it registers.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// forgeDefault is the git host repositories are checked out from. WHICH host
// this deployment's repositories live on is infra wiring, so it is env with a
// constant default — not a policy row, and never a request field: see checkout,
// where the owner segment is the validated principal's org and the caller
// supplies only a slug.
const forgeDefault = "https://git.hanzo.ai"

// state is the subsystem: the shared Base plus the warm-workspace pool. It holds
// no org in a field — the org is a parameter on every call, so one process serves
// all orgs and an org can never be captured from stale state.
type state struct {
	cloud.Base
	forge string
	pool  *pool
}

var mounted *state

// Mount wires /v1/lsp onto app per HIP-0106.
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
	s := &state{Base: cloud.NewBase(deps, "lsp"), forge: forge()}
	s.pool = newPool(s.build)
	mounted = s

	if err := routes(app, s); err != nil {
		return err
	}

	s.Log.Info("lsp surface mounted (native)",
		"brand", deps.Brand, "forge", s.forge, "languages", len(table))
	return nil
}

// routes registers the /v1/lsp surface: ONE typed op, because there is one value.
// Typed rather than raw so the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method are all projected from this one entry — a
// surface built for coding agents, where the MCP tool is the point.
func routes(app cloud.Router, s *state) error {
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	//
	// Grouped at "/v1" with "/lsp" as the member, the shape apps/bots and
	// apps/account already use for a route that IS its prefix. The obvious
	// spelling — Group("/v1/lsp") with an empty member — addresses "/v1/lsp/",
	// a different path from the "/v1/lsp" the manifest row publishes, and that
	// one-character mismatch between what the binary serves and what the host
	// routes is invisible until a client 404s.
	g := app.Group("/v1")
	zip.Post(g, "/lsp", s.ask)
	return nil
}

// Shutdown closes every warm workspace: each is a live subprocess and a directory
// on disk, and neither is reclaimed by the process exiting cleanly. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	mounted.pool.closeAll()
	mounted = nil
	return nil
}

// Invalidate drops every warm revision of one repository, for when a push or a
// re-index makes a checkout stale.
//
// It is exported and unused IN THIS BINARY, which is the honest state of it:
// apps/code and apps/lsp are separate processes, so code cannot call this
// in-process when it re-indexes, and the push signal has to arrive over the bus.
// That is phase 2. It is mostly self-correcting meanwhile — workspaces are keyed
// by revision, so new content is a new key — and the gap is a caller that tracks
// a branch while the branch moves.
func Invalidate(org, repo string) {
	if mounted != nil {
		mounted.pool.drop(org, repo)
	}
}

func forge() string {
	if v := strings.TrimSpace(os.Getenv("LSP_FORGE_URL")); v != "" {
		return v
	}
	return forgeDefault
}
