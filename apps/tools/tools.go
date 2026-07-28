package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/types"
)

// Mount wires the unified tool plane at /v1/tools/* and installs the two providers
// this package OWNS — builtin (full-cloud-control over the live route table) and
// the external-MCP-server source. Every OTHER source (connectors, functions,
// agents, skills) registers its own Provider from its own Mount via tools.Register,
// so this package never learns how another source lists or runs its tools.
//
// The process-wide registry (std) is populated by those Register calls regardless
// of mount order; Mount only installs the activation store + this package's
// providers + the HTTP surface.

const (
	// feeEnvPrefix prices one tools-plane dispatch unit per deployment (0 ⇒ free).
	feeEnvPrefix = "CLOUD_TOOLS_FEE_CENTS"
	meterKind    = "call"
)

// state is the tools subsystem's own data: the activation + external-server stores,
// the KMS client (external-server auth custody), and the audit recorder.
type state struct {
	activation *ActivationStore
	servers    *MCPServerStore
	kms        types.KMSClient
	audit      *audit.Recorder
}

var mounted *cloud.Service[state]

// Mount registers the tool plane on app.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tools.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("tools.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("tools.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("tools.Mount: data dir: %w", err)
	}
	activation, err := OpenActivationStore(filepath.Join(deps.DataDir, "tools-activation.db"))
	if err != nil {
		return fmt.Errorf("tools.Mount: open activation store: %w", err)
	}
	servers, err := OpenMCPServerStore(filepath.Join(deps.DataDir, "tools-mcp.db"))
	if err != nil {
		_ = activation.Close()
		return fmt.Errorf("tools.Mount: open mcp-server store: %w", err)
	}

	// Install the activation store on the process-wide registry and register the two
	// providers this package owns.
	std.SetActivation(activation)
	std.Register(newBuiltinProvider(app))
	std.Register(newMCPProvider(servers, deps.KMS))

	b := cloud.NewBase(deps, "tools")
	s := &cloud.Service[state]{Base: b, State: state{
		activation: activation,
		servers:    servers,
		kms:        deps.KMS,
		audit:      deps.Audit,
	}}
	mounted = s

	routes(app, s)
	b.Log.Info("tools plane mounted", "prefix", "/v1/tools", "brand", deps.Brand, "kms", deps.KMS != nil)
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	// Collection root (/v1/tools) stays flat — Group(p).Get("") yields "p/".
	app.Get("/v1/tools", cloud.Handle(s, listTools))
	g := app.Group("/v1/tools")
	g.Post("/mcp", cloud.Handle(s, mcp))
	g.Get("/activation", cloud.Handle(s, getActivation))
	g.Put("/activation", cloud.Handle(s, putActivation))
	g.Get("/servers", cloud.Handle(s, listServers))
	g.Post("/servers", cloud.Handle(s, createServer))
	g.Delete("/servers/:id", cloud.Handle(s, deleteServer))
}

// Shutdown closes the stores. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var first error
	if mounted.State.activation != nil {
		if err := mounted.State.activation.Close(); err != nil {
			first = err
		}
	}
	if mounted.State.servers != nil {
		if err := mounted.State.servers.Close(); err != nil && first == nil {
			first = err
		}
	}
	mounted = nil
	return first
}
