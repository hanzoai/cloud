package script

import (
	"context"
	"net/http"
	"sync/atomic"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off the typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// known tells a connector this runtime has no program for from an id no connector
// carries. The host hands it over at Use (the provider app's registry), so this
// package imports no app.
var known atomic.Pointer[func(id string) bool]

// Missing is the refusal for an id this runtime holds no program for: a known
// connector is 422 (it exists, nothing here runs it yet), anything else, or no
// predicate wired, is 404.
func Missing(id string) error {
	if k := known.Load(); k != nil && (*k)(id) {
		return zip.Errorf(http.StatusUnprocessableEntity, "%s is a known connector but has no action that runs here yet", id)
	}
	return zip.Errorf(http.StatusNotFound, "unknown provider %q", id)
}

// Mount wires the native single-provider execution surface onto the cloud
// binary, per HIP-0126 / HIP-0106:
//
//	✓ POST /v1/auto/provider/:id/run   run one provider action in-process
//
// This is the native replacement for the standalone ActivePieces Node engine's
// /v1/auto/providers/{provider}/run — same {action,auth,props} -> {ok,output,error}
// contract, executed in goja in-process (no `auto` pod). It is org-gated: only
// a validated principal may run a provider, and the caller's resolved
// credential travels in the request `auth`. The route is DISTINCT from
// automations' GET /v1/auto/provider (the catalogue), so the two
// subsystems compose without collision. It is a TYPED op — one registry entry
// from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow.
func Use(app cloud.Router, deps cloud.Deps, isKnown func(id string) bool) error {
	known.Store(&isKnown)
	log := luxlog.Default()
	g := app.Group("/v1/auto/provider")
	// cloud.Bridge is not installed here. Whoever composes the program installs it
	// once at the root — after the identity check that mints the validated org and
	// before any subsystem registers a route (serve.go) — because that order is a
	// property of the whole program and no subsystem can assert it for itself. The
	// validated org still reaches the typed op only off the context, never as an
	// In field, which is caller-supplied.
	g.Post("/:id/run", run)
	if log != nil {
		log.New("subsystem", "script").Info(
			"provider runtime mounted", "programs", len(Programs()))
	}
	return nil
}

// runIn is one provider-action invocation: the provider from the path, the
// ActivePieces-shaped {action, auth, props} provider-run body.
type runIn struct {
	// ID is the provider to run, from the path.
	ID string `json:"id"`
	// Action is the name of the provider action to invoke.
	Action string `json:"action"`
	// Auth is the caller's resolved credential for the provider, handed to the
	// action verbatim. Its shape is whatever the provider's auth descriptor
	// declares (a token string, an object), so it is opaque here.
	Auth any `json:"auth"`
	// Props are the action's input properties, keyed by property name.
	Props map[string]any `json:"props"`
}

// runResp mirrors the ActivePieces provider-run response. A provider-level
// failure is ok:false with a message (HTTP 200) — an unknown provider or a
// missing action is a 4xx, matching the old engine's infra-vs-provider split.
type runResp struct {
	// Ok reports whether the action ran to completion.
	Ok bool `json:"ok"`
	// Output is the action's result when ok. Its shape is the action's own.
	Output any `json:"output,omitempty"`
	// Error is the provider-level failure message when not ok.
	Error string `json:"error,omitempty"`
}

// Run executes one provider action in-process and answers the outcome. The
// caller's resolved credential travels in `auth`, delivered to the action
// verbatim — the runtime resolves no credential itself. An action that ran and
// failed (or an action name the provider does not have) answers ok:false with
// the failure message, not an HTTP error; an unknown provider is 404, a known
// connector with no program here 422, and a missing action 422.
//
// Example: {"id": "notion", "action": "create_page", "auth": "secret-token", "props": {"title": "Hello"}}
func run(ctx context.Context, in *runIn) (*runResp, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !Has(in.ID) {
		return nil, Missing(in.ID)
	}
	if in.Action == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "action is required")
	}
	out, err := Run(ctx, org, in.ID, in.Action, in.Auth, in.Props)
	if err != nil {
		// The action ran but failed (or the action name is unknown): surface as
		// a provider-level failure, not an infra 5xx — the caller inspects ok.
		return &runResp{Ok: false, Error: err.Error()}, nil
	}
	return &runResp{Ok: true, Output: out}, nil
}
