package knowledge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// lexBody wraps text in a minimal Lexical EditorState so the after_save hook's
// lexicalText recovers it (and its wikilinks) verbatim.
func lexBody(text string) string {
	return `{"root":{"type":"root","children":[{"type":"paragraph","children":[{"type":"text","text":"` + text + `"}]}]}}`
}

// edgeCount returns the number of kb-link edges whose source is the named page, read
// through the generic framework list surface — proving edges are ordinary framework
// documents (no parallel store), not a graph-only projection.
func edgeCount(t *testing.T, app *zip.App, org, source string) int {
	t.Helper()
	filters := url.QueryEscape(`{"source":"` + source + `"}`)
	code, b := req(t, app, http.MethodGet, "/v1/framework/kb-link?filters="+filters, org, nil)
	if code != http.StatusOK {
		t.Fatalf("list kb-link: %d %s", code, b)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("decode kb-link list: %v (%s)", err, b)
	}
	return len(resp.Data)
}

func graphOf(t *testing.T, app *zip.App, org string) graphResp {
	t.Helper()
	code, b := req(t, app, http.MethodGet, "/v1/kb/graph", org, nil)
	if code != http.StatusOK {
		t.Fatalf("graph: %d %s", code, b)
	}
	var g graphResp
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("decode graph: %v (%s)", err, b)
	}
	return g
}

func (g graphResp) hasEdge(from, to, kind string) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func (g graphResp) hasNode(id string) bool {
	for _, n := range g.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// TestIndexFailureDoesNotSkipLinks proves the index and link hooks are independent:
// with the vector store unreachable (indexing fails), the page still saves AND its
// wikilinks are still extracted into edges. runHooks stops at the first erroring
// hook, so this would regress if index and link were separate ordered hooks.
func TestIndexFailureDoesNotSkipLinks(t *testing.T) {
	// Point the indexer at a closed port so ensureCollection/upsert fails, while the
	// (fake) embedder still succeeds — indexOnSave returns an error.
	t.Setenv("vectorEndpoint", "http://127.0.0.1:9")
	t.Setenv("KB_EMBED_DIMS", "8")
	kbAI = fakeAI{dims: 8}
	idxOnce = sync.Once{}
	idx = nil
	_ = index()

	app := mountKB(t)
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}
	// The page write must succeed (indexing is best-effort) and the wikilink edge must
	// still be created despite the index failure.
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A",
		map[string]any{"title": "Alpha", "slug": "alpha", "body": lexBody("links to [[Beta]]")}); code != http.StatusCreated {
		t.Fatalf("create alpha: %d %s", code, b)
	}
	if n := edgeCount(t, app, "A", "alpha"); n != 1 {
		t.Fatalf("wikilink edge must survive an index outage: got %d edges, want 1", n)
	}
}

// TestWikilinkLifecycle proves the whole edge lifecycle end-to-end: extraction on
// create, reconciliation on edit (removed links are deleted), cleanup on the source
// page's trash, and — the key value-not-place property — a target's trash turning a
// resolved link into a dangling one with NO edge rewrite.
func TestWikilinkLifecycle(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}

	// Beta exists; Alpha links to Beta (resolves) and Gamma (dangling).
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A",
		map[string]any{"title": "Beta", "slug": "beta", "body": lexBody("beta content")}); code != http.StatusCreated {
		t.Fatalf("create beta: %d %s", code, b)
	}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A",
		map[string]any{"title": "Alpha", "slug": "alpha", "body": lexBody("see [[Beta]] and [[Gamma]]")}); code != http.StatusCreated {
		t.Fatalf("create alpha: %d %s", code, b)
	}

	// Two edges extracted; graph resolves one and dangles the other.
	if n := edgeCount(t, app, "A", "alpha"); n != 2 {
		t.Fatalf("after create: want 2 edges, got %d", n)
	}
	g := graphOf(t, app, "A")
	if !g.hasEdge("kb-page:alpha", "kb-page:beta", "link") {
		t.Errorf("missing resolved edge alpha→beta")
	}
	if !g.hasEdge("kb-page:alpha", "unresolved:gamma", "link") {
		t.Errorf("missing dangling edge alpha→gamma")
	}

	// Edit Alpha to drop the Gamma link → reconcile deletes that edge.
	if code, b := req(t, app, http.MethodPut, "/v1/framework/kb-page/alpha", "A",
		map[string]any{"title": "Alpha", "slug": "alpha", "body": lexBody("only [[Beta]] now")}); code != http.StatusOK {
		t.Fatalf("update alpha: %d %s", code, b)
	}
	if n := edgeCount(t, app, "A", "alpha"); n != 1 {
		t.Fatalf("after edit: want 1 edge, got %d", n)
	}
	g = graphOf(t, app, "A")
	if g.hasNode("unresolved:gamma") {
		t.Errorf("gamma should be gone after edit; nodes=%+v", g.Nodes)
	}

	// Target-trash consistency: trashing Beta turns alpha→beta into a dangling link,
	// with NO edge rewrite (resolution is by value at read time).
	if code, b := req(t, app, http.MethodDelete, "/v1/framework/kb-page/beta", "A", nil); code != http.StatusNoContent {
		t.Fatalf("delete beta: %d %s", code, b)
	}
	if n := edgeCount(t, app, "A", "alpha"); n != 1 {
		t.Fatalf("edge must persist after target trash (value not place): got %d", n)
	}
	g = graphOf(t, app, "A")
	if g.hasNode("kb-page:beta") {
		t.Errorf("beta node should be gone")
	}
	if !g.hasEdge("kb-page:alpha", "unresolved:beta", "link") {
		t.Errorf("alpha→beta should now be a dangling link; edges=%+v", g.Edges)
	}

	// Source-trash cleanup: trashing Alpha removes its outgoing edges.
	if code, b := req(t, app, http.MethodDelete, "/v1/framework/kb-page/alpha", "A", nil); code != http.StatusNoContent {
		t.Fatalf("delete alpha: %d %s", code, b)
	}
	if n := edgeCount(t, app, "A", "alpha"); n != 0 {
		t.Fatalf("after source trash: want 0 edges, got %d", n)
	}
}
