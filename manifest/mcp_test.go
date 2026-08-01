package manifest

// The gates under the fleet's ONE agent door.
//
// POST /v1/mcp is the host's, served by zip from the composed plugin catalogues
// (cmd/cloud/main.go, plugin/embed.go). Three things can silently take it away,
// and each one is pinned here:
//
//  1. an App row claiming /v1/mcp — a Load registers All(prefix) + All(prefix/*),
//     and fiber MERGES byte-identical patterns into one route with both handlers
//     chained, so the host's own POST would sit BEHIND a proxy handler and never
//     run. Silent shadowing, not a panic.
//  2. a plugin growing a SECOND hand-rolled door — this fleet had three MCP tool
//     registries for one concept, and the way back is one route registration.
//  3. a catalogue naming a tool no op answers, or two plugins naming one tool.

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mcpDoor is the ONE public MCP path. The host claims it exactly, so a plugin
// prefix may be DEEPER (tools owns /v1/mcp/servers) but never equal.
const mcpDoor = "/v1/mcp"

// TestNoAppClaimsTheDoor: no manifest row may claim the exact door path.
func TestNoAppClaimsTheDoor(t *testing.T) {
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if p == mcpDoor {
				t.Fatalf("app %q claims %q, the host's own MCP door. A Load there registers "+
					"All(%q), which fiber merges with the host's POST into one route — the door "+
					"would sit behind the proxy handler and never run. Claim a DEEPER prefix "+
					"(%q/servers) or none.", a.Name, p, p, p)
			}
		}
	}
}

// TestNoSecondMCPDoor: no app may serve a path ENDING in /mcp.
//
// This is the structural reason a fourth registry cannot grow back. A hand-rolled
// JSON-RPC door can only exist as a route; every route an app serves is
// regenerated into its own subset by the drift gate (mk/fleet.mk surface-check);
// and the one true door is a zip CONTROL route, which is in no subset at all. So
// the next hand-rolled envelope turns this red and the message names the door it
// should have used instead.
//
// A path CONTAINING /mcp is fine — /v1/mcp/servers is the external MCP server
// registry, a real and different capability (records an org creates, not tools a
// registry enumerates).
func TestNoSecondMCPDoor(t *testing.T) {
	for _, a := range Apps {
		for _, p := range served(t, a.Name) {
			if strings.HasSuffix(p, "/mcp") {
				t.Errorf("app %q serves %q. The fleet has ONE MCP door: POST /v1/mcp on the host, "+
					"composed from every plugin's build-time catalogue. A typed op is already a "+
					"tool there — register one instead of a second JSON-RPC envelope.", a.Name, p)
			}
		}
	}
}

// foreignDoors is the CLOSED list of routes ending in /mcp that this fleet serves
// and that are NOT a projection of our own typed ops. Each entry carries why.
//
// The list exists because the document cannot see every door: a subsystem that
// mounts a raw net/http mux registers routes zip never projects, so a path can be
// served and appear in no subset. TestNoSecondMCPDoorInSource reads the SOURCE
// for that reason, and a door with a real, foreign owner is named here rather
// than deleted — deleting it would remove a capability with nothing to replace it.
var foreignDoors = map[string]string{
	// hanzoai/tasks' OWN MCP surface, served by the embedded engine's
	// srv.MCPHandler() behind cloud's identity gate. Its tools are the task/workflow
	// engine's, implemented in that module — they are not cloud typed ops, so the
	// host's catalogue could not carry them and the one door does not duplicate
	// them. Same class as apps/tools' EXTERNAL MCP server registry: a real
	// capability with a different owner, reached through this fleet rather than
	// projected from it.
	"apps/tasks/tasks.go": "hanzoai/tasks' own engine tool surface (srv.MCPHandler), not a projection of cloud's typed ops",

	// hanzoai/world's OWN MCP surface (serverInfo "hanzo-world"; the world-brief and
	// market-radar tools live in that module's internal/world/mcp), reached at
	// /v1/world/mcp because the ingress carves that path off the cloud catch-all to
	// world-gw. Same class as tasks — a real capability with a different owner —
	// with one difference worth stating: cloud does not SERVE this door, it only
	// NAMES it. apps/world/index.go holds the address as a data value in the front
	// door's wire list, because a route cloud does not route can appear in no
	// document (openapi.Describe renders prose only for a served route), so that op
	// is the only place a caller can discover it. No JSON-RPC envelope is registered
	// here and none may be.
	"apps/world/index.go": "hanzoai/world's own MCP tool surface, served by world-gw via an ingress path-carve; cloud names the address, never serves it",
}

// TestNoSecondMCPDoorInSource is the gate that makes a fourth registry impossible
// to add quietly. A hand-rolled door has to register a route, and a route needs a
// path literal — so any Go string ending in "/mcp" outside cmd/cloud is either the
// one door being moved (a deliberate edit here) or a rival being born.
//
// It reads SOURCE, which is what lets it see what the document cannot: apps/tasks'
// door is a raw net/http mux handler and appears in no subset at all.
func TestNoSecondMCPDoorInSource(t *testing.T) {
	lit := regexp.MustCompile(`"[a-z0-9/_:.-]*/mcp"`)
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
				if _, ok := foreignDoors[filepath.ToSlash(rel)]; ok {
					continue
				}
				t.Errorf("%s:%d serves an MCP path: %s\n"+
					"The fleet has ONE MCP door — POST /v1/mcp on the host, composed from every "+
					"plugin's build-time catalogue. A typed op is ALREADY a tool there. If this is a "+
					"foreign engine's own surface rather than a projection of our ops, name it in "+
					"foreignDoors with the reason.", rel, i+1, trimmed)
			}
			return nil
		})
	}
}

// TestEveryCatalogueToolIsAnOpOfItsOwnApp: soundness of the composed door.
//
// mcp.json and openapi.json are two projections of ONE registry taken in one
// process at one instant (`<app> describe`), so this cannot fail while both are
// regenerated together — which is precisely why it is worth asserting: it is the
// cheap, no-build check that a HAND-EDITED catalogue, or one left behind by a
// half-run generator, does not publish a tool the owning app cannot answer.
//
// It also refuses two apps claiming one tool name. A name is dispatch, so a
// duplicate is unroutable; zip refuses it at Load (App.installTools), which is a
// boot failure. Catching it here makes it a red build instead.
func TestEveryCatalogueToolIsAnOpOfItsOwnApp(t *testing.T) {
	owner := map[string]string{}
	tools := 0
	for _, a := range Apps {
		ops := operationIDs(t, a.Name)
		for _, name := range catalogue(t, a.Name) {
			tools++
			if _, ok := ops[name]; !ok {
				t.Errorf("%s/mcp.json names tool %q, which is not an operationId in "+
					"%s/openapi.json. The catalogue is a projection of the same typed-op "+
					"registry the document is — regenerate: make -f mk/fleet.mk describe-apps",
					a.Name, name, a.Name)
			}
			if held, dup := owner[name]; dup {
				t.Errorf("tool %q is claimed by both %q and %q. A tool name is dispatch, so two "+
					"owners make it unroutable and zip refuses the composition at boot — rename "+
					"one op's operationId.", name, held, a.Name)
			}
			owner[name] = a.Name
		}
	}
	if tools == 0 {
		t.Fatal("the fleet's composed MCP door would carry ZERO tools — no plugin/<app>/mcp.json holds any")
	}
	t.Logf("%d MCP tools across %d apps on the one door", tools, len(Apps))
}

// catalogue is the tool names in an app's committed MCP catalogue.
func catalogue(t *testing.T, app string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "plugin", app, "mcp.json"))
	if err != nil {
		t.Fatalf("%s: %v\n\nEvery app publishes its own MCP catalogue beside its subset. "+
			"Run `make -f mk/fleet.mk describe-apps`.", app, err)
	}
	var tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("%s/mcp.json: %v", app, err)
	}
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		// An op present with an EMPTY description is a SILENT failure: the model
		// pays context for a nameless tool it cannot choose. That exact bug shipped
		// once here (zipdoc blind to group prefixes), so it is a gate, not a hope.
		if strings.TrimSpace(tl.Description) == "" {
			t.Errorf("%s tool %q has an EMPTY description — the prose zipdoc lifts IS what a "+
				"model reads to pick it. Write the doc comment and run: go generate -run zipdoc "+
				"./apps/%s/...", app, tl.Name, app)
		}
		if len(tl.InputSchema) == 0 {
			t.Errorf("%s tool %q has no inputSchema", app, tl.Name)
		}
		out = append(out, tl.Name)
	}
	return out
}

// operationIDs is every operationId an app's own subset publishes.
func operationIDs(t *testing.T, app string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "plugin", app, "openapi.json"))
	if err != nil {
		t.Fatalf("%s: %v", app, err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s/openapi.json: %v", app, err)
	}
	out := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range item {
			if op.OperationID != "" {
				out[op.OperationID] = true
			}
		}
	}
	return out
}
