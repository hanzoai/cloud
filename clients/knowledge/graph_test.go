package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/framework"
)

// doc is a tiny constructor for a framework.Document in buildGraph unit tests.
func doc(name string, data map[string]any) framework.Document {
	return framework.Document{Name: name, Data: data}
}

// TestBuildGraph_ShapesNodesAndEdges is the pure proof of the graph shaping:
// parent tree, wikilink resolution by title, a dangling link as an "unresolved"
// node, connector provenance, and a floating memory node.
func TestBuildGraph_ShapesNodesAndEdges(t *testing.T) {
	pages := []framework.Document{
		doc("home", map[string]any{"title": "Home", "project": "p1"}),
		doc("runbook", map[string]any{"title": "Runbook", "parent": "home"}),
	}
	memories := []framework.Document{doc("m1", map[string]any{"title": "a memory"})}
	sources := []framework.Document{doc("s1", map[string]any{"title": "README", "provider": "github"})}
	connectors := []framework.Document{doc("github", map[string]any{"provider": "github"})}
	links := []framework.Document{
		// home → Runbook (resolves by title), home → Ghost (dangling).
		doc("e1", map[string]any{"source": "home", "target_title": "Runbook"}),
		doc("e2", map[string]any{"source": "home", "target_title": "Ghost"}),
		// an edge from a page not in the set is ignored.
		doc("e3", map[string]any{"source": "elsewhere", "target_title": "Runbook"}),
	}

	nodes, edges := buildGraph(pages, memories, sources, connectors, links)

	nodeByID := map[string]graphNode{}
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	mustNode := func(id, typ string) {
		if n, ok := nodeByID[id]; !ok || n.Type != typ {
			t.Fatalf("node %q (type %s) missing; nodes=%+v", id, typ, nodes)
		}
	}
	mustNode("kb-page:home", DTPage)
	mustNode("kb-page:runbook", DTPage)
	mustNode("kb-memory:m1", DTMemory)
	mustNode("kb-source:s1", DTSource)
	mustNode("kb-connector:github", DTConnector)
	mustNode("unresolved:ghost", "unresolved")

	has := func(from, to, kind string) bool {
		for _, e := range edges {
			if e.From == from && e.To == to && e.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has("kb-page:runbook", "kb-page:home", "parent") {
		t.Errorf("missing parent edge; edges=%+v", edges)
	}
	if !has("kb-page:home", "kb-page:runbook", "link") {
		t.Errorf("missing resolved wikilink edge; edges=%+v", edges)
	}
	if !has("kb-page:home", "unresolved:ghost", "link") {
		t.Errorf("missing dangling wikilink edge; edges=%+v", edges)
	}
	if !has("kb-source:s1", "kb-connector:github", "provenance") {
		t.Errorf("missing provenance edge; edges=%+v", edges)
	}
	// The edge from a page outside the set must NOT appear.
	for _, e := range edges {
		if e.From == "kb-page:elsewhere" {
			t.Errorf("edge from out-of-set page leaked: %+v", e)
		}
	}
}

type graphResp struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// TestGraphEndpoint_EndToEnd drives the REAL HTTP surface: creating pages fires the
// after_save hook that extracts wikilinks into kb-link edges, and GET /v1/kb/graph
// returns the resolved graph. Cross-org isolation is asserted too.
func TestGraphEndpoint_EndToEnd(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}

	lexBody := func(text string) string {
		return `{"root":{"type":"root","children":[{"type":"paragraph","children":[{"type":"text","text":"` + text + `"}]}]}}`
	}
	// Home links to Runbook (will resolve) and to Ghost (dangling).
	home := map[string]any{"title": "Home", "slug": "home", "body": lexBody("See [[Runbook]] and [[Ghost]]")}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A", home); code != http.StatusCreated {
		t.Fatalf("create home: %d %s", code, b)
	}
	runbook := map[string]any{"title": "Runbook", "slug": "runbook", "parent": "home", "body": lexBody("Back to [[Home]]")}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A", runbook); code != http.StatusCreated {
		t.Fatalf("create runbook: %d %s", code, b)
	}

	code, b := req(t, app, http.MethodGet, "/v1/kb/graph", "A", nil)
	if code != http.StatusOK {
		t.Fatalf("graph: %d %s", code, b)
	}
	var g graphResp
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("decode graph: %v (%s)", err, b)
	}

	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, want := range []string{"kb-page:home", "kb-page:runbook", "unresolved:ghost"} {
		if !ids[want] {
			t.Errorf("graph missing node %q; got %+v", want, g.Nodes)
		}
	}
	has := func(from, to, kind string) bool {
		for _, e := range g.Edges {
			if e.From == from && e.To == to && e.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has("kb-page:runbook", "kb-page:home", "parent") {
		t.Errorf("missing parent edge: %+v", g.Edges)
	}
	if !has("kb-page:home", "kb-page:runbook", "link") {
		t.Errorf("missing resolved wikilink edge (home→runbook): %+v", g.Edges)
	}
	if !has("kb-page:runbook", "kb-page:home", "link") {
		t.Errorf("missing resolved wikilink edge (runbook→home): %+v", g.Edges)
	}
	if !has("kb-page:home", "unresolved:ghost", "link") {
		t.Errorf("missing dangling wikilink edge: %+v", g.Edges)
	}

	// Cross-org: B's graph is empty (never sees A's knowledge).
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "B", nil); code != http.StatusOK {
		t.Fatalf("install B: %d %s", code, b)
	}
	code, b = req(t, app, http.MethodGet, "/v1/kb/graph", "B", nil)
	if code != http.StatusOK {
		t.Fatalf("graph B: %d %s", code, b)
	}
	var gb graphResp
	_ = json.Unmarshal(b, &gb)
	if len(gb.Nodes) != 0 || len(gb.Edges) != 0 {
		t.Fatalf("CROSS-ORG LEAK: B sees %d nodes, %d edges", len(gb.Nodes), len(gb.Edges))
	}
}

// TestGraphRefusesWithoutPrincipal proves the graph surface refuses a forged org
// with no validated principal.
func TestGraphRefusesWithoutPrincipal(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	hr := httptest.NewRequest(http.MethodGet, "/v1/kb/graph", nil)
	hr.Header.Set("X-Org-Id", "victim") // forged org, no validated principal
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged-org graph must be 403, got %d", resp.StatusCode)
	}
}
