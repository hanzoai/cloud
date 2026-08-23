// Copyright © 2026 Hanzo AI. MIT License.

package ai

// mcp.go is ai's half of the fleet's ONE agent MCP server.
//
// THE AGGREGATION ALREADY EXISTS AND NOTHING HERE REBUILDS IT. A typed op is a
// tool — zip projects the same registry entry into the REST route, the OpenAPI
// operation, the CLI command and the MCP tool, and the host composes every
// subsystem's projection into one array it serves at POST /v1/mcp. That is why
// there is no tool list in this file and no schema written by hand: the
// declaration is the source, and a second list beside it could only ever drift
// from the first. What was missing is not a registry. It is the ANSWER to "what
// is on the MCP server", which until now no process could give and no test
// asserted — and an inventory nobody can read is how an MCP server that serves
// nothing passes for a healthy one.
//
// So this file adds exactly one op, and it reports what THIS PROCESS's MCP server
// actually carries — read from the live registry, never from a description of it.
//
// IT USED TO REPORT A THIRD NUMBER, `published`: every tool the BUILD could
// serve, summed over the committed plugin/<app>/mcp.json catalogues. Those files
// are gone. They were a second source for a fact each child already knows, and
// they were wrong — plugin/o11y/mcp.json held 12 tools while the o11y binary at
// the same commit served 365 — so the fleet-wide question is answered by ASKING
// the fleet now, at the one MCP address, which also NAMES every subsystem it
// could not reach (package fleet). No process but the host can ask that
// question, and a subsystem inventing an answer to it is precisely the green
// surface this file was written against.
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
// instead — the fleet MCP server refuses to serve one name from two apps and logs
// both (fleet/mcp.go). A hand-written id in this package therefore carries its
// subsystem: aiMCPTools, never mcpTools.

import (
	"context"
	"sort"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// mcpGate is the refusal, spelled ONCE. The MCP server renders a handler error as
// isError content carrying err.Error(); the REST route renders the same error as
// a 403 whose msg is the same string. One value, so a caller refused at one
// projection is refused at the other in the same words — which is the property
// mcp_test.go asserts, because "a tool is an API call" is worth nothing if it is
// only ever a comment.
const mcpGate = "sign in to read this deployment's MCP tool surface"

// mcpOps binds the op to the app whose MCP server it reports. A TypedHandler is
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

// aiMCPQuery asks for the tool names as well as the counts.
//
// A typed op's Go type name IS its schema name and the fleet's schema namespace
// is FLAT, so every name in this file carries the product prefix.
type aiMCPQuery struct {
	// Names asks for this process's tool NAMES and not only how many there are.
	// Off by default: a list of names is a page, and the question this op exists to
	// answer ("is the MCP server up and does it have anything behind it") is
	// answered by the count.
	Names bool `json:"names"`
}

// aiMCPSurface is what the one MCP server carries, from this process's vantage.
type aiMCPSurface struct {
	// Tools is how many tools THIS PROCESS's MCP server carries: its own typed-op
	// registry, projected. It is the only number a subsystem can state honestly —
	// what the FLEET's server carries is a question only the host can ask, and it
	// asks it by asking every subsystem (POST /v1/mcp, tools/list).
	Tools int `json:"tools"`
	// Apps is one row per subsystem this deployment composes, in manifest order.
	Apps []aiMCPApp `json:"apps"`
	// Names are this process's own tool names, present only when the query asked
	// for them.
	Names []string `json:"names,omitempty"`
}

// aiMCPApp is one subsystem, as this process sees it.
type aiMCPApp struct {
	// Name is the subsystem, as the manifest names it.
	Name string `json:"name"`
	// Served reports that THIS process mounted it, so its tools are on this
	// process's MCP server rather than behind a sibling this process only knows the
	// name of.
	Served bool `json:"served"`
}

// Tools reports what THIS PROCESS's MCP server carries: how many tools its own
// registry projects, optionally their names, and which subsystems this process
// composed. It is the answer to "is this MCP server up and does it have anything
// behind it" — a question a status code cannot answer, since an empty server and
// a full one are both 200. What the FLEET's server carries is the fleet server's
// own answer: POST /v1/mcp, tools/list, which asks every subsystem and names the
// ones that did not reply.
func (o mcpOps) tools(ctx context.Context, in *aiMCPQuery) (*aiMCPSurface, error) {
	// A TOOL CALL IS AN API CALL. The gate is the op's own, read from the bit
	// cloud.Bridge parked, so it holds identically over REST and over MCP — and
	// fails closed off the HTTP path, where there is no attested caller at all.
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrForbidden(mcpGate)
	}
	return surface(o.app, in.Names), nil
}

// surface reads the MCP server: this process's own registry, and which subsystems
// it actually composed.
//
// Both halves come from the LIVE app — App.MCPTools and App.Plugins — never from
// a list of what was meant to mount, and never from an artifact. There is no
// build-time half left to disagree with them.
func surface(app *zip.App, names bool) *aiMCPSurface {
	tools := app.MCPTools()
	mounted := map[string]bool{}
	for _, p := range app.Plugins() {
		mounted[p.Name] = true
	}
	out := &aiMCPSurface{Tools: len(tools), Apps: make([]aiMCPApp, 0, len(manifest.Apps))}
	for _, a := range manifest.Apps {
		out.Apps = append(out.Apps, aiMCPApp{Name: a.Name, Served: mounted[a.Name]})
	}
	if names {
		out.Names = make([]string, 0, len(tools))
		for _, t := range tools {
			out.Names = append(out.Names, t["name"].(string))
		}
		sort.Strings(out.Names)
	}
	return out
}
