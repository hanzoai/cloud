package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// Mount wires the unified tool plane at /v1/tool/* and installs the providers
// this package OWNS — the external-MCP-server source, the brand's skills (asked
// of the skills app over the plane) and the org's own authored skills. Every
// OTHER source (connectors, functions, agents) registers its own Provider from
// its own Mount via tools.Register, so this package never learns how another
// source lists or runs its tools.
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
	mcp        *mcpProvider   // the servers' tools, listed and called with their credential (external_mcp.go)
	authored   *AuthoredStore // org-authored plugins (pluginbuild.go)
	skills     *SkillStore    // org-authored skills (skillstore.go)
	catalog    *CatalogStore  // the canonical copy of the public registries (catalog.go)
	keeper     *keeper        // refreshes catalog when a read finds it stale (catalog_keep.go)
	kms        secret.Store
	ai         infer.Client // generates plugin source from an API spec; nil ⇒ source-only builds
	audit      *audit.Recorder
}

var mounted *cloud.Service[state]

// Mount registers the tool plane on app.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("tools: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("tools.Use:  empty DataDir")
	}
	activation, err := OpenActivationStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("tools.Use:  open activation store: %w", err)
	}
	servers, err := OpenMCPServerStore(cloud.DataDir(), deps.MasterKey, deps.KMS)
	if err != nil {
		_ = activation.Close()
		return fmt.Errorf("tools.Use:  open mcp-server store: %w", err)
	}
	authored, err := OpenAuthoredStore(cloud.DataDir())
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		return fmt.Errorf("tools.Use:  open authored-plugin store: %w", err)
	}

	skills, err := OpenSkillStore(cloud.DataDir())
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		_ = authored.Close()
		return fmt.Errorf("tools.Use:  open skill store: %w", err)
	}

	catalog, err := OpenCatalogStore(cloud.DataDir())
	if err != nil {
		_ = activation.Close()
		_ = servers.Close()
		_ = authored.Close()
		_ = skills.Close()
		return fmt.Errorf("tools.Use:  open catalog store: %w", err)
	}

	b := cloud.NewBase(deps, "tools")

	// Install the activation store on the process-wide registry and register the
	// providers this package owns: the external-MCP-server source, the brand's
	// skills (asked of the skills app, skills_peer.go) and the org's own authored
	// skills. The brand's register first so an equal-rank name collision resolves
	// to the brand's skill, never the org's.
	mcp := newMCPProvider(servers, deps.KMS)
	std.SetActivation(activation)
	std.Register(mcp)
	std.Register(newBrandSkills(b.Log))
	std.Register(orgSkillProvider{store: skills})
	s := &cloud.Service[state]{Base: b, State: state{
		activation: activation,
		servers:    servers,
		mcp:        mcp,
		authored:   authored,
		skills:     skills,
		catalog:    catalog,
		keeper:     newKeeper(catalog, b.Log),
		kms:        deps.KMS,
		ai:         deps.AI,
		audit:      deps.Audit,
	}}
	mounted = s
	serveSkills()
	serveSkillDocs()
	serveKit()
	serveRelay()
	serveMarket()

	routes(app, s)
	b.Log.Info("tools plane mounted", "prefix", "/v1/tool", "brand", cloud.Brand(), "kms", deps.KMS != nil)
	return nil
}

// routes declares the tool plane on ONE group, /v1.
//
// cloud.Bridge is NOT installed here: Serve installs it once for the whole binary
// (serve.go), after the identity boundary that makes the org trustworthy and
// before UseAll registers any of these leaves. fiber runs middleware in
// registration order, so one installed here — below the app-level Use — would be
// redundant, and one installed after these leaves would never run.
//
// TYPED ops are all of them but one; the one that could not be described without
// moving its wire is registered untyped below, named at its own definition with
// the reason (pluginbuild.go's buildPlugin).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := toolOps{s: s}

	v1 := app.Group("/v1/tool")
	// Discovery + activation.
	v1.Get("", o.listTools)
	// The catalog (catalog.go): our canonical copy of what the public MCP
	// registries publish, and the two decisions we make about each entry. It hangs
	// under /v1/tool because it is the SHELF this plane's servers are picked
	// from — one noun, not a fifth top-level one.
	v1.Get("/catalog", o.listCatalog)
	v1.Post("/catalog/sync", o.syncCatalog)
	v1.Get("/catalog/:id", o.getListing)
	v1.Patch("/catalog/:id", o.curateListing)
	v1.Get("/activation", o.getActivation)
	v1.Put("/activation", o.putActivation)
	// The caller's kit (kit.go): what their coding runs carry, and their own
	// layer on it, which only narrows the admin's.
	v1.Get("/kit", o.getKit)
	v1.Put("/kit", o.putKit)
	v1.Post("/detect", o.detectTools)
	v1.Post("/call", o.callTool)

	// The separately-listed registries (see registries.go): skills and mcp are
	// Source views of the SAME registry; plugins is the mounted-subsystem
	// inventory, which is a different thing entirely. All three are views of this
	// plane, so all three hang under it — the rows they read are tools' own.
	v1.Get("/skills", o.listSkills)
	v1.Post("/skills", o.putSkill, zip.WithStatus(http.StatusCreated))
	v1.Get("/skills/authored", o.listAuthoredSkills)
	v1.Delete("/skills/:id", o.deleteSkill)
	v1.Get("/plugins", o.listPlugins)

	// The builder (pluginbuild.go). /v1/tool/plugins lists what this deployment
	// mounted; /authored is what THIS ORG built, which is a different set with a
	// different lifecycle, so it is a subpath rather than a mixed collection.
	v1.Post("/plugins/build", o.buildPlugin, zip.WithStatus(http.StatusCreated))
	v1.Get("/plugins/authored", o.listAuthoredPlugins)
	v1.Delete("/plugins/authored/:id", o.deleteAuthoredPlugin)

	// The external MCP server registry: a server is a record an org creates, not a
	// tool the registry enumerates. /v1/mcp is the HOST's agent MCP address,
	// wire-fixed for every MCP client (HIP-0139 §3.2), so this plane vacates that
	// root entirely and keeps its servers where its rows are.
	v1.Get("/mcp/servers", o.listServers)
	v1.Post("/mcp/servers", o.createServer, zip.WithStatus(http.StatusCreated))
	v1.Delete("/mcp/servers/:id", o.deleteServer)
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
	if mounted.State.keeper != nil {
		mounted.State.keeper.stop()
	}
	if mounted.State.catalog != nil {
		if err := mounted.State.catalog.Close(); err != nil && first == nil {
			first = err
		}
	}
	mounted = nil
	return first
}
