// Copyright © 2026 Hanzo AI. MIT License.

package ai

// mcp.go is ai's half of the fleet's ONE agent door.
//
// THE AGGREGATION ALREADY EXISTS AND NOTHING HERE REBUILDS IT. A typed op is a
// tool — zip projects the same registry entry into the REST route, the OpenAPI
// operation, the CLI command and the MCP tool, and the host composes every
// subsystem's projection into one array it serves at POST /v1/mcp. That is why
// there is no tool list in this file and no schema written by hand: the
// declaration is the source, and a second list beside it could only ever drift
// from the first. What was missing is not a registry. It is the ANSWER to "what
// is on the door", which until now no process could give and no test asserted —
// and an inventory nobody can read is how a door that serves nothing passes for
// a healthy one.
//
// So this file adds exactly one op, and it reports THREE numbers that are three
// different questions, never one number standing in for all of them:
//
//	published — every tool this BUILD can serve: the union of every subsystem's
//	            committed catalogue, the same plugin/<app>/mcp.json bytes the host
//	            hands zip at Load. A property of the artifact, true in any process.
//	served    — what THIS PROCESS's door actually composed. Read from the LIVE
//	            composition (App.Plugins + App.MCPTools), so a subsystem that did
//	            not mount is missing from it. This is the number that can be zero
//	            while `published` is nine hundred, and saying so is the whole point.
//	local     — the part of `served` this process registered itself, as opposed to
//	            composing from a child's catalogue.
//
// A deployment manifest answers what was INTENDED; only the process answers what
// it LOADED, and during a rolling upgrade the two disagree by design (scope.go
// says the same thing about Plugins()). Publishing one and calling it the other
// is the green-surface bug this fleet keeps shipping.
//
// NAMESPACE. A tool's name is its operationId — the same string the REST route's
// document publishes, because they are one value with three projections. Two
// subsystems cannot collide, in two layers: an op that does not name itself gets
// the PATH-derived id (get_v1_o11y_logs, get_v1_analytics_top), and a path lives
// under the subtree the manifest grants exactly one subsystem, so two defaults
// cannot meet; an op that DOES name itself escapes that, so the name is checked
// instead — zip refuses a Load whose catalogue claims a name another plugin
// already owns (a boot failure), and manifest's TestEveryCatalogueToolIsAnOpOfItsOwnApp
// turns that boot failure into a red build. A hand-written id in this package
// therefore carries its subsystem: aiMCPTools, never mcpTools.

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plugin"
	"github.com/zap-proto/zip"
)

// mcpGate is the refusal, spelled ONCE. The MCP door renders a handler error as
// isError content carrying err.Error(); the REST route renders the same error as
// a 403 whose msg is the same string. One value, so a caller refused at one
// projection is refused at the other in the same words — which is the property
// mcp_test.go asserts, because "a tool is an API call" is worth nothing if it is
// only ever a comment.
const mcpGate = "sign in to read this deployment's MCP tool surface"

// mcpOps binds the op to the app whose door it reports. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the app — so the
// app arrives as a RECEIVER, which is also the only bound form cmd/zipdoc can
// lift prose from.
type mcpOps struct{ app *zip.App }

// zipdoc lifts the doc comment off the typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time, and a tool whose
// description is empty is one a model pays context for and cannot choose. Run by
// `make -C apps/ai generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mountMCP registers ai's one MCP op on the app it reports.
//
// Registered on the app rather than a group, at its absolute path, because ai's
// own mount ends in a greedy All("/v1/*") and a static route must win over it:
// the router matches by ServeMux-1.22 specificity, so /v1/ai/mcp/tools beats
// /v1/* whichever registered first, and registering here — before that wildcard
// goes up — keeps the reading order the same as the routing order.
func mountMCP(app *zip.App) {
	o := mcpOps{app: app}
	zip.Get(app, "/v1/ai/mcp/tools", o.tools, zip.WithOperationID("aiMCPTools"))
}

// aiMCPQuery narrows the answer to one subsystem.
//
// A typed op's Go type name IS its schema name and the fleet's schema namespace
// is FLAT, so every name in this file carries the product prefix.
type aiMCPQuery struct {
	// App names one subsystem whose tool NAMES to list. Empty answers counts
	// only: nine hundred names is a page no operator reads and no model can
	// afford to be handed by accident.
	App string `json:"app"`
}

// aiMCPSurface is what the one MCP door carries, from this process's vantage.
type aiMCPSurface struct {
	// Published is every tool this BUILD can serve — the union of every
	// subsystem's committed catalogue, which is a property of the artifact and
	// therefore the same answer in every process.
	Published int `json:"published"`
	// Served is what THIS PROCESS's door actually composed. It is the number that
	// can be far smaller than Published — a host that mounted nothing serves
	// nothing — and the only one that describes the door a client is talking to.
	Served int `json:"served"`
	// Local is the part of Served this process registered ITSELF, rather than
	// composing from a mounted child's catalogue.
	Local int `json:"local"`
	// Apps is one row per subsystem the build publishes, in manifest order.
	Apps []aiMCPApp `json:"apps"`
}

// aiMCPApp is one subsystem's contribution to the door.
type aiMCPApp struct {
	// Name is the subsystem, as the manifest names it.
	Name string `json:"name"`
	// Tools is how many tools its committed catalogue publishes.
	Tools int `json:"tools"`
	// Served reports that THIS process actually mounted it, so its tools are on
	// the door a client can call rather than only in the build.
	Served bool `json:"served"`
	// Names are its tool names, present only for the subsystem the query named.
	Names []string `json:"names,omitempty"`
}

// Tools reports what this binary's MCP door carries: every tool the build
// publishes, how many of them this process actually serves, and which subsystem
// each belongs to. It is the answer to "is the door up and does it have anything
// behind it" — a question a status code cannot answer, since an empty door and a
// full one are both 200.
func (o mcpOps) tools(ctx context.Context, in *aiMCPQuery) (*aiMCPSurface, error) {
	// A TOOL CALL IS AN API CALL. The gate is the op's own, read from the bit
	// cloud.Bridge parked, so it holds identically over REST and over MCP — and
	// fails closed off the HTTP path, where there is no attested caller at all.
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrForbidden(mcpGate)
	}
	return surface(o.app, in.App), nil
}

// surface reads the door. The published half comes from the committed
// catalogues — the same bytes the host hands zip — and the served half from the
// live composition, never from a list of what was meant to mount.
func surface(app *zip.App, only string) *aiMCPSurface {
	cat := published()
	mounted := map[string]bool{}
	for _, p := range app.Plugins() {
		mounted[p.Name] = true
	}
	out := &aiMCPSurface{
		Local: len(app.MCPTools()),
		Apps:  make([]aiMCPApp, 0, len(manifest.Apps)),
	}
	out.Served = out.Local
	for _, a := range manifest.Apps {
		row := aiMCPApp{Name: a.Name, Tools: len(cat[a.Name]), Served: mounted[a.Name]}
		if row.Served {
			out.Served += row.Tools
		}
		if only != "" && only == a.Name {
			row.Names = cat[a.Name]
		}
		out.Published += row.Tools
		out.Apps = append(out.Apps, row)
	}
	return out
}

// published is the build's catalogue — subsystem → its tool names — parsed ONCE.
// The bytes are the artifact each app's own binary projected from its own
// registry at build time (plugin/embed.go), so this reads what the door serves
// rather than a description of it.
var published = sync.OnceValue(func() map[string][]string {
	out := make(map[string][]string, len(manifest.Apps))
	for _, a := range manifest.Apps {
		var tools []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(plugin.Tools(a.Name), &tools) != nil {
			continue // an app that has not been described yet publishes nothing
		}
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		out[a.Name] = names
	}
	return out
})
