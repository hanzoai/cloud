package manifest

// The gates under the fleet's ONE agent MCP server.
//
// POST /v1/mcp is the host's, composed by ASKING every subsystem what it serves
// (cmd/cloud/main.go, package fleet). Two things can silently take it away, and
// each one is pinned here:
//
//  1. an App row claiming /v1/mcp — a Load registers All(prefix) + All(prefix/*),
//     and fiber MERGES byte-identical patterns into one route with both handlers
//     chained, so the host's own POST would sit BEHIND a proxy handler and never
//     run. Silent shadowing, not a panic.
//  2. a plugin growing a SECOND hand-rolled MCP server — this fleet had three MCP
//     tool registries for one concept, and the way back is one route registration.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mcpEndpoint is the ONE public MCP path — MCPPath, not a literal restating it. This
// file guards the endpoint; a guard that spells the address itself can pass while
// the endpoint has moved, which is the drift these gates exist to make impossible.
// The host claims the path exactly, so a plugin prefix may be DEEPER (tools owns
// /v1/mcp/servers) but never equal.
const mcpEndpoint = MCPPath

// TestNoAppClaimsTheEndpoint: no manifest row may claim the exact MCP path.
func TestNoAppClaimsTheEndpoint(t *testing.T) {
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if p == mcpEndpoint {
				t.Fatalf("app %q claims %q, the host's own MCP endpoint. A Load there registers "+
					"All(%q), which fiber merges with the host's POST into one route — the endpoint "+
					"would sit behind the proxy handler and never run. Claim a DEEPER prefix "+
					"(%q/servers) or none.", a.Name, p, p, p)
			}
		}
	}
}

// TestNoSecondMCPEndpoint: no app may serve a path ENDING in /mcp.
//
// This is the structural reason a fourth registry cannot grow back. A hand-rolled
// JSON-RPC endpoint can only exist as a route; every route an app serves is
// regenerated into its own subset by the drift gate (mk/fleet.mk check);
// and the one true endpoint is the host's own route, which is in no subset at all.
// So the next hand-rolled envelope turns this red and the message names the
// endpoint it should have used instead.
//
// A path CONTAINING /mcp is fine — /v1/mcp/servers is the external MCP server
// registry, a real and different capability (records an org creates, not tools a
// registry enumerates).
func TestNoSecondMCPEndpoint(t *testing.T) {
	for _, a := range Apps {
		for _, p := range served(t, a.Name) {
			if strings.HasSuffix(p, "/mcp") {
				t.Errorf("app %q serves %q. The fleet has ONE MCP server: POST /v1/mcp on the host, "+
					"composed by asking every subsystem. A typed op is already a tool there — "+
					"register one instead of a second JSON-RPC envelope.", a.Name, p)
			}
		}
	}
}

// foreignEndpoints is the CLOSED list of routes ending in /mcp that this fleet serves
// and that are NOT a projection of our own typed ops. Each entry carries why.
//
// The list exists because the document cannot see every endpoint: a subsystem that
// mounts a raw net/http mux registers routes zip never projects, so a path can be
// served and appear in no subset. TestNoSecondMCPEndpointInSource reads the SOURCE
// for that reason, and an endpoint with a real, foreign owner is named here rather
// than deleted — deleting it would remove a capability with nothing to replace it.
var foreignEndpoints = map[string]string{
	// hanzoai/tasks' OWN MCP surface, served by the embedded engine's
	// srv.MCPHandler() behind cloud's identity gate. Its tools are the task/workflow
	// engine's, implemented in that module — they are not cloud typed ops, so the
	// host's catalogue could not carry them and the one MCP server does not
	// duplicate them. Same class as apps/tools' EXTERNAL MCP server registry: a real
	// capability with a different owner, reached through this fleet rather than
	// projected from it.
	"apps/tasks/tasks.go": "hanzoai/tasks' own engine tool surface (srv.MCPHandler), not a projection of cloud's typed ops",

	// hanzoai/world's OWN MCP surface (serverInfo "hanzo-world"; the world-brief and
	// market-radar tools live in that module's internal/world/mcp), reached at
	// /v1/world/mcp because the ingress carves that path off the cloud catch-all to
	// world-gw. Same class as tasks — a real capability with a different owner —
	// with one difference worth stating: cloud does not SERVE this MCP server, it
	// only NAMES it. apps/world/index.go holds the address as a data value in the
	// index op's wire list, because a route cloud does not route can appear in no
	// document (openapi.Describe renders prose only for a served route), so that op
	// is the only place a caller can discover it. No JSON-RPC envelope is registered
	// here and none may be.
	"apps/world/index.go": "hanzoai/world's own MCP tool surface, served by world-gw via an ingress path-carve; cloud names the address, never serves it",
}

// TestNoSecondMCPEndpointInSource is the gate that makes a fourth registry impossible
// to add quietly. A hand-rolled MCP server has to register a route, and a route
// needs a path literal — so any Go string ending in "/mcp" outside cmd/cloud is
// either the one endpoint being moved (a deliberate edit here) or a rival being
// born.
//
// It reads SOURCE, which is what lets it see what the document cannot: apps/tasks'
// MCP server is a raw net/http mux handler and appears in no subset at all.
func TestNoSecondMCPEndpointInSource(t *testing.T) {
	// A ROUTE path, which is what an MCP server needs and what this gate hunts: it
	// starts at the root and ends at /mcp. The leading slash is load-bearing —
	// without it the pattern also matched `"github.com/zap-proto/mcp"`, and an import
	// of the PROTOCOL is the opposite of a rival MCP server: webui's terminal handler
	// imports it precisely so it can hand a frame to zip's existing MCP server
	// instead of writing an envelope of its own.
	lit := regexp.MustCompile(`"/(?:[a-z0-9/_:.-]*/)?mcp"`)
	root := filepath.Join("..")
	for _, dir := range []string{"apps", "clients", "webui"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for i, line := range strings.Split(string(src), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || !lit.MatchString(line) {
					continue
				}
				if _, ok := foreignEndpoints[filepath.ToSlash(rel)]; ok {
					continue
				}
				t.Errorf("%s:%d serves an MCP path: %s\n"+
					"The fleet has ONE MCP server — POST /v1/mcp on the host, composed by asking "+
					"every subsystem. A typed op is ALREADY a tool there. If this is a "+
					"foreign engine's own surface rather than a projection of our ops, name it in "+
					"foreignEndpoints with the reason.", rel, i+1, trimmed)
			}
			return nil
		})
	}
}

// THE CATALOGUE GATE IS GONE, WITH THE CATALOGUE.
//
// TestEveryCatalogueToolIsAnOpOfItsOwnApp read plugin/<app>/mcp.json and checked
// each name against that app's openapi.json — two artifacts generated in one
// process at one instant, so it could only ever catch a hand edit or a half-run
// generator. It could not catch the failure that mattered: BOTH files stale by
// the same 353 ops, which is exactly how o11y shipped. Two derived things agreeing
// with each other is not evidence about the thing they derive from.
//
// The question it asked — "is every tool on the MCP server an op of its owning
// app?" — is now unaskable, and that is the point: the MCP server IS the apps' own
// registries, asked at the moment of asking, so a tool that is not an op of its
// app cannot be on it. What replaces the gate is a test of the live mechanism,
// against running subsystems, which goes red when a subsystem's tools go missing
// or when two apps claim one name: surface/mcp_test.go.
