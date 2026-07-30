package tools

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The two routes on this plane that cannot be typed ops still have to be
// DESCRIBED. "Carries no zip registry entry" was being read as "publishes
// nothing", and those are opposite facts: both rendered as an operationId and a
// tag and NO BODY AT ALL, which is exactly what a route taking no input and
// returning none publishes. No consumer of the document can tell the two apart,
// so every SDK generated off openapi.yaml offered an MCP call with nowhere to put
// the JSON-RPC envelope and a plugin build with nowhere to put the source.
//
// openapi.Register is the seam for the halves that ARE statable. It attaches to a
// route the router already carries, so it can never contradict the router, and it
// does NOT make these typed ops: there is still no prose, no MCP tool, no CLI
// command and no SDK method, because those come from zip's registry alone. See
// untypedByDesign in typed_wire_test.go for why each one stays out of it.
//
// What is still NOT declared here, honestly — each a missing capability, not a
// missing edit, so none is papered over with prose that overstates:
//
//   - the builder's 422 diagnostics. apply states the success shape under the
//     "2XX" range key only; the failure body needs the same declarable-error-body
//     capability that keeps this route untyped in the first place.
//   - the MCP envelope's per-method result. One response schema cannot say that
//     `result` is server info for initialize and a tools array for tools/list, so
//     it is declared as what it is: unconstrained JSON.
//   - field prose on all four shapes. zipdoc lifts doc comments off TYPED ops
//     only, and Register's reflection seam reads Go types, not comments — so
//     buildOut publishes `bytes: integer` with no description. AuthoredPlugin is
//     unaffected: it is already described through the typed ops that share it,
//     and Fold merges the typed schema over this one.
func init() {
	openapi.Register("/v1/tools/mcp", "POST", mcpRequest{}, mcpResponse{})
	openapi.Register("/v1/plugins/build", "POST", buildRequest{}, buildOut{})
}

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
	authored   *AuthoredStore // org-authored plugins (pluginbuild.go)
	skills     *SkillStore    // org-authored skills (skillstore.go)
	kms        types.KMSClient
	ai         types.AIClient // generates plugin source from an API spec; nil ⇒ source-only builds
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
	authored, err := OpenAuthoredStore(filepath.Join(deps.DataDir, "tools-plugins.db"))
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		return fmt.Errorf("tools.Mount: open authored-plugin store: %w", err)
	}

	skills, err := OpenSkillStore(filepath.Join(deps.DataDir, "tools-skills.db"))
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		_ = authored.Close()
		return fmt.Errorf("tools.Mount: open skill store: %w", err)
	}

	// Install the activation store on the process-wide registry and register the
	// providers this package owns: builtin, the external-MCP-server source, and
	// the org's own skills.
	std.SetActivation(activation)
	std.Register(newBuiltinProvider(app))
	std.Register(newMCPProvider(servers, deps.KMS))
	std.Register(orgSkillProvider{store: skills})

	b := cloud.NewBase(deps, "tools")
	s := &cloud.Service[state]{Base: b, State: state{
		activation: activation,
		servers:    servers,
		authored:   authored,
		skills:     skills,
		kms:        deps.KMS,
		ai:         deps.AI,
		audit:      deps.Audit,
	}}
	mounted = s

	routes(app, s)
	b.Log.Info("tools plane mounted", "prefix", "/v1/tools", "brand", deps.Brand, "kms", deps.KMS != nil)
	return nil
}

// routes declares the tool plane on ONE group, /v1.
//
// This subsystem spans four top-level nouns — tools, skills, mcp and plugins —
// so there is no single prefix to hang a group on, and a group per noun could not
// carry the collection ROOTS anyway (Group(p).Get("") yields "p/"). One /v1 group
// spells each path exactly as the wire spells it, and it is also the OpTarget the
// typed ops are declared on, so zip composes each op's path from the same prefix
// the router does and cmd/zipdoc resolves it the same way.
//
// cloud.Bridge is NOT installed here: Serve installs it once for the whole binary
// (serve.go), after the identity boundary that makes the org trustworthy and
// before MountAll registers any of these leaves. fiber runs middleware in
// registration order, so one installed here — below the app-level Use — would be
// redundant, and one installed after these leaves would never run.
//
// TYPED ops are the 14 that could be described without moving their wire; the two
// that could not are registered untyped below, each named at its own definition
// with the reason (http.go's mcp, pluginbuild.go's buildPlugin).
func routes(app cloud.Router, s *cloud.Service[state]) {
	v1 := app.Group("/v1")
	o := toolOps{s: s}

	// Discovery + activation.
	zip.Get(v1, "/tools", o.listTools)
	zip.Get(v1, "/tools/activation", o.getActivation)
	zip.Put(v1, "/tools/activation", o.putActivation)
	v1.Post("/tools/mcp", cloud.Handle(s, mcp))

	// The separately-listed registries (see registries.go): skills and mcp are
	// Source views of the SAME registry; plugins is the mounted-subsystem
	// inventory, which is a different thing entirely.
	zip.Get(v1, "/skills", o.listSkills)
	zip.Post(v1, "/skills", o.putSkill, zip.WithStatus(http.StatusCreated))
	zip.Get(v1, "/skills/authored", o.listAuthoredSkills)
	zip.Delete(v1, "/skills/:id", o.deleteSkill)
	zip.Get(v1, "/mcp", o.listMCPTools)
	zip.Get(v1, "/plugins", o.listPlugins)

	// The builder (pluginbuild.go). /v1/plugins lists what this deployment
	// mounted; /authored is what THIS ORG built, which is a different set with a
	// different lifecycle, so it is a subpath rather than a mixed collection.
	v1.Post("/plugins/build", cloud.Handle(s, buildPlugin))
	zip.Get(v1, "/plugins/authored", o.listAuthoredPlugins)
	zip.Delete(v1, "/plugins/authored/:id", o.deleteAuthoredPlugin)

	// The external MCP server registry lives with /v1/mcp, not under /v1/tools:
	// a server is a record an org creates, not a tool the registry enumerates.
	zip.Get(v1, "/mcp/servers", o.listServers)
	zip.Post(v1, "/mcp/servers", o.createServer, zip.WithStatus(http.StatusCreated))
	zip.Delete(v1, "/mcp/servers/:id", o.deleteServer)
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
	if mounted.State.authored != nil {
		if err := mounted.State.authored.Close(); err != nil && first == nil {
			first = err
		}
	}
	if mounted.State.skills != nil {
		if err := mounted.State.skills.Close(); err != nil && first == nil {
			first = err
		}
	}
	mounted = nil
	return first
}
