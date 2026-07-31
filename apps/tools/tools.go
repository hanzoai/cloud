package tools

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
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

	if err := routes(app, s); err != nil {
		return err
	}
	b.Log.Info("tools plane mounted", "prefix", "/v1/tools", "brand", deps.Brand, "kms", deps.KMS != nil)
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) error {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("tools.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — fiber runs middleware in registration order — bounded to
	// the tool plane's own subtree, which the bare collection path is part of.
	app.Group("/v1/tools").Use(cloud.Bridge())

	// Collection root (/v1/tools) stays flat: the full path is spelled on the op.
	zip.Get(zapp, "/v1/tools", o.listTools)
	zip.Get(zapp, "/v1/tools/activation", o.getActivation)
	zip.Put(zapp, "/v1/tools/activation", o.putActivation)
	zip.Get(zapp, "/v1/tools/servers", o.listServers)
	zip.Post(zapp, "/v1/tools/servers", o.createServer, zip.WithStatus(http.StatusCreated))
	zip.Delete(zapp, "/v1/tools/servers/:id", o.deleteServer)

	// POST /v1/tools/mcp stays a RAW handler. It is JSON-RPC: an undecodable body
	// must answer HTTP 200 carrying a -32700 error object, and a tool-level failure
	// answers 200 with an error object too — a contract a typed op cannot express,
	// because zip decodes In before the handler runs and rejects a bad body with
	// 400, and it emits exactly one (success) response schema. Typing it would
	// break every MCP client rather than document it.
	app.Post("/v1/tools/mcp", cloud.Handle(s, mcp))
	return nil
}

// ops binds the service to the tool plane's typed handlers: a TypedHandler has no
// parameter for the service, so it arrives as a RECEIVER — also the one bound form
// cmd/zipdoc lifts prose from.
type ops struct{ s *cloud.Service[state] }

// None is the input of an op that takes none: no body, no query, no path param.
type None struct{}

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
