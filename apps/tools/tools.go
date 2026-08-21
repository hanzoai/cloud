package tools

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The ONE route on this plane that cannot be a typed op still has to be
// DESCRIBED. "Carries no zip registry entry" was being read as "publishes
// nothing", and those are opposite facts: it rendered as an operationId and a
// tag and NO BODY AT ALL, which is exactly what a route taking no input and
// returning none publishes. No consumer of the document can tell the two apart,
// so every SDK generated off openapi.yaml offered a plugin build with nowhere to
// put the source.
//
// openapi.Register is the seam for the BODIES; openapi.Describe is the seam for
// the PROSE, which zipdoc lifts from a doc comment for a typed op and has nowhere
// to lift from here. Both attach to a route the router already carries, so neither
// can contradict the router, and neither makes this a typed op: it is still not an
// MCP tool and still not a CLI command, because those come from zip's registry
// alone. See untypedByDesign in typed_wire_test.go for why it stays out of it.
//
// What is still NOT declared here, honestly — each a missing capability, not a
// missing edit, so none is papered over with prose that overstates:
//
//   - the builder's 422 diagnostics. apply states the success shape under the
//     "2XX" range key only; the failure body needs the same declarable-error-body
//     capability that keeps this route untyped in the first place. The prose below
//     names it, which is the honest half a schema cannot carry.
//   - field prose on both shapes. zipdoc lifts doc comments off TYPED ops
//     only, and Register's reflection seam reads Go types, not comments — so
//     buildOut publishes `bytes: integer` with no description. AuthoredPlugin is
//     unaffected: it is already described through the typed ops that share it,
//     and Fold merges the typed schema over this one.
func init() {
	openapi.Register("/v1/tools/plugins/build", "POST", buildRequest{}, buildOut{})
	openapi.Describe("/v1/tools/plugins/build", http.MethodPost,
		"Build a plugin for your org from TypeScript, or from an API spec a model writes it from",
		"Builds one plugin for the caller's org and answers 201 with the bundle's size, whether a "+
			"model wrote the source, and the plugin as stored. Post `source` to build TypeScript "+
			"as-is, or `spec` — an OpenAPI document or plain prose describing the endpoints — to "+
			"have one generated; the generated source comes back in the answer, so a caller reads "+
			"what will run before it runs. Exactly one of the two, and `name` must be one lowercase "+
			"path segment; both or neither is 400.\n\n"+
			"COMPILING IS THE GATE. The source goes through the same pipeline the committed "+
			"connectors do — esbuild to one CommonJS program, then compiled in the goja runtime "+
			"that will actually execute it — and anything that fails is rejected and NEVER stored. "+
			"So a plugin in the store is one this deployment has already loaded once, not one a "+
			"model claimed was fine. A failed build answers 422 carrying the diagnostics a caller "+
			"needs to fix it: the bundler's error, the source that failed, and whether the model "+
			"wrote it — a body outside the declared success shape.\n\n"+
			"CREDENTIALS ARE NOT PART OF A PLUGIN. A plugin names the connectors `provider` it "+
			"needs and reads that credential from `ctx.auth` at run time, under KMS custody. Source "+
			"that contains something shaped like a key is REFUSED rather than silently scrubbed, so "+
			"a caller who pasted one finds out instead of shipping it — register it as a connector "+
			"instead.\n\n"+
			"Requires a validated principal; 403 without one. The plugin is stored under that "+
			"principal's org and is what `/v1/tools/plugins/authored` lists — never "+
			"`/v1/tools/plugins`, which "+
			"is this deployment's mounted-subsystem inventory. Source over 512 KiB or a spec over "+
			"256 KiB is refused. Posting a `spec` to a deployment with no AI client configured is "+
			"503, and a generation that fails upstream is 502.")
}

// Mount wires the unified tool plane at /v1/tools/* and installs the two providers
// this package OWNS — the external-MCP-server source and the org's own authored
// skills. Every OTHER source (connectors, functions, agents, agent skills)
// registers its own Provider from its own Mount via tools.Register, so this
// package never learns how another source lists or runs its tools.
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
	catalog    *CatalogStore  // the canonical copy of the public registries (catalog.go)
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
	if deps.DataDir == "" {
		return fmt.Errorf("tools.Mount: empty DataDir")
	}
	activation, err := OpenActivationStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("tools.Mount: open activation store: %w", err)
	}
	servers, err := OpenMCPServerStore(deps.DataDir)
	if err != nil {
		_ = activation.Close()
		return fmt.Errorf("tools.Mount: open mcp-server store: %w", err)
	}
	authored, err := OpenAuthoredStore(deps.DataDir)
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		return fmt.Errorf("tools.Mount: open authored-plugin store: %w", err)
	}

	skills, err := OpenSkillStore(deps.DataDir)
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		_ = authored.Close()
		return fmt.Errorf("tools.Mount: open skill store: %w", err)
	}

	catalog, err := OpenCatalogStore(deps.DataDir)
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		_ = authored.Close()
		_ = skills.Close()
		return fmt.Errorf("tools.Mount: open catalog store: %w", err)
	}

	// Install the activation store on the process-wide registry and register the
	// providers this package owns: the external-MCP-server source and the org's
	// own authored skills.
	std.SetActivation(activation)
	std.Register(newMCPProvider(servers, deps.KMS))
	std.Register(orgSkillProvider{store: skills})

	b := cloud.NewBase(deps, "tools")
	s := &cloud.Service[state]{Base: b, State: state{
		activation: activation,
		servers:    servers,
		authored:   authored,
		skills:     skills,
		catalog:    catalog,
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
// Every path below is under /v1/tools, and the group is still /v1 rather than
// /v1/tools because the plane's COLLECTION ROOT is /v1/tools itself: joining a
// /v1/tools group with an empty leaf yields /v1/tools/, an address this API has
// never served. One /v1 group spells each path exactly as the wire spells it,
// and it is also the OpTarget the typed ops are declared on, so zip composes each
// op's path from the same prefix the router does and cmd/zipdoc resolves it the
// same way.
//
// cloud.Bridge is NOT installed here: Serve installs it once for the whole binary
// (serve.go), after the identity boundary that makes the org trustworthy and
// before MountAll registers any of these leaves. fiber runs middleware in
// registration order, so one installed here — below the app-level Use — would be
// redundant, and one installed after these leaves would never run.
//
// TYPED ops are all of them but one; the one that could not be described without
// moving its wire is registered untyped below, named at its own definition with
// the reason (pluginbuild.go's buildPlugin).
func routes(app cloud.Router, s *cloud.Service[state]) {
	v1 := app.Group("/v1")
	o := toolOps{s: s}

	// Discovery + activation.
	zip.Get(v1, "/tools", o.listTools)
	// The catalog (catalog.go): our canonical copy of what the public MCP
	// registries publish, and the two decisions we make about each entry. It hangs
	// under /v1/tools because it is the SHELF this plane's servers are picked
	// from — one noun, not a fifth top-level one.
	zip.Get(v1, "/tools/catalog", o.listCatalog)
	zip.Post(v1, "/tools/catalog/sync", o.syncCatalog)
	zip.Get(v1, "/tools/catalog/:id", o.getListing)
	zip.Patch(v1, "/tools/catalog/:id", o.curateListing)
	zip.Get(v1, "/tools/activation", o.getActivation)
	zip.Put(v1, "/tools/activation", o.putActivation)
	zip.Post(v1, "/tools/call", o.callTool)

	// The separately-listed registries (see registries.go): skills and mcp are
	// Source views of the SAME registry; plugins is the mounted-subsystem
	// inventory, which is a different thing entirely. All three are views of this
	// plane, so all three hang under it — the rows they read are tools' own.
	zip.Get(v1, "/tools/skills", o.listSkills)
	zip.Post(v1, "/tools/skills", o.putSkill, zip.WithStatus(http.StatusCreated))
	zip.Get(v1, "/tools/skills/authored", o.listAuthoredSkills)
	zip.Delete(v1, "/tools/skills/:id", o.deleteSkill)
	zip.Get(v1, "/tools/plugins", o.listPlugins)

	// The builder (pluginbuild.go). /v1/tools/plugins lists what this deployment
	// mounted; /authored is what THIS ORG built, which is a different set with a
	// different lifecycle, so it is a subpath rather than a mixed collection.
	v1.Post("/tools/plugins/build", cloud.Handle(s, buildPlugin))
	zip.Get(v1, "/tools/plugins/authored", o.listAuthoredPlugins)
	zip.Delete(v1, "/tools/plugins/authored/:id", o.deleteAuthoredPlugin)

	// The external MCP server registry: a server is a record an org creates, not a
	// tool the registry enumerates. /v1/mcp is the HOST's agent door, wire-fixed
	// for every MCP client (HIP-0139 §3.2), so this plane vacates that root
	// entirely and keeps its servers where its rows are.
	zip.Get(v1, "/tools/mcp/servers", o.listServers)
	zip.Post(v1, "/tools/mcp/servers", o.createServer, zip.WithStatus(http.StatusCreated))
	zip.Delete(v1, "/tools/mcp/servers/:id", o.deleteServer)
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
	if mounted.State.catalog != nil {
		if err := mounted.State.catalog.Close(); err != nil && first == nil {
			first = err
		}
	}
	mounted = nil
	return first
}
