package connectorruntime

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off the typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount wires the native single-connector execution surface onto the cloud
// binary, per HIP-0126 / HIP-0106:
//
//	✓ POST /v1/automations/connectors/:id/run   run one connector action in-process
//
// This is the native replacement for the standalone ActivePieces Node engine's
// /v1/auto/pieces/{piece}/run — same {action,auth,props} -> {ok,output,error}
// contract, executed in goja in-process (no `auto` pod). It is org-gated: only
// a validated principal may run a connector, and the caller's resolved
// credential travels in the request `auth`. The route is DISTINCT from
// automations' GET /v1/automations/connectors (the catalogue), so the two
// subsystems compose without collision. It is a TYPED op — one registry entry
// from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("connectorruntime.Mount: nil app")
	}
	log := deps.Logger
	g := app.Group("/v1/automations/connectors")
	// cloud.Bridge is not installed here. Whoever composes the program installs it
	// once at the root — after the identity check that mints the validated org and
	// before any subsystem registers a route (serve.go) — because that order is a
	// property of the whole program and no subsystem can assert it for itself. The
	// validated org still reaches the typed op only off the context, never as an
	// In field, which is caller-supplied.
	zip.Post(g, "/:id/run", run)
	if log != nil {
		log.New("subsystem", "connectorruntime").Info(
			"connector runtime mounted", "connectors", len(Connectors()))
	}
	return nil
}

// runIn is one connector-action invocation: the connector from the path, the
// ActivePieces-shaped {action, auth, props} piece-run body.
type runIn struct {
	// ID is the connector to run, from the path.
	ID string `json:"id"`
	// Action is the name of the connector action to invoke.
	Action string `json:"action"`
	// Auth is the caller's resolved credential for the connector, handed to the
	// action verbatim. Its shape is whatever the connector's auth descriptor
	// declares (a token string, an object), so it is opaque here.
	Auth any `json:"auth"`
	// Props are the action's input properties, keyed by property name.
	Props map[string]any `json:"props"`
}

// runResp mirrors the ActivePieces piece-run response. A connector-level
// failure is ok:false with a message (HTTP 200) — an unknown connector or a
// missing action is a 4xx, matching the old engine's infra-vs-piece split.
type runResp struct {
	// Ok reports whether the action ran to completion.
	Ok bool `json:"ok"`
	// Output is the action's result when ok. Its shape is the action's own.
	Output any `json:"output,omitempty"`
	// Error is the connector-level failure message when not ok.
	Error string `json:"error,omitempty"`
}

// Run executes one connector action in-process and answers the outcome. The
// caller's resolved credential travels in `auth`, delivered to the action
// verbatim — the runtime resolves no credential itself. An action that ran and
// failed (or an action name the connector does not have) answers ok:false with
// the failure message, not an HTTP error; an unknown connector is 404 and a
// missing action 422.
//
// Example: {"id": "notion", "action": "create_page", "auth": "secret-token", "props": {"title": "Hello"}}
func run(ctx context.Context, in *runIn) (*runResp, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	if !Has(in.ID) {
		return nil, zip.Errorf(http.StatusNotFound, "unknown connector %q", in.ID)
	}
	if in.Action == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "action is required")
	}
	out, err := Run(ctx, org, in.ID, in.Action, in.Auth, in.Props)
	if err != nil {
		// The action ran but failed (or the action name is unknown): surface as
		// a piece-level failure, not an infra 5xx — the caller inspects ok.
		return &runResp{Ok: false, Error: err.Error()}, nil
	}
	return &runResp{Ok: true, Output: out}, nil
}
