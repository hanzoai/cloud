package world

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// index_test.go holds the index to the two properties that make it worth
// serving: it answers WITHOUT a principal (discovery precedes credentials), and
// every wire it names is a real address under this product's own prefix.

// TestIndexIsPublic proves GET /v1/world answers the anonymous caller. Every other
// read here except limits is org-scoped and 403s; the index must not be, or a
// caller with no token still cannot find out what World is.
func TestIndexIsPublic(t *testing.T) {
	app, _ := mountWorld(t)

	code, raw := do(t, app, http.MethodGet, "/v1/world", "", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/world: want 200, got %d (%s)", code, raw)
	}
	var got worldIndex
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Product == "" || got.Summary == "" {
		t.Fatalf("the index names nothing: product=%q summary=%q", got.Product, got.Summary)
	}
}

// TestIndexNamesEveryWireCompletely is the anti-rot gate. The wire list is the ONE
// place /v1/world/mcp and /v1/world/zap are stated in this product's surface — the
// generated document cannot carry them, because cloud does not route them — so a
// half-filled entry here is a dead end for the only consumer that could have
// followed it. Every field except Spec is required, and every path must sit under
// this product's own prefix: an index that pointed somewhere else would be
// re-inventing the routing table the manifest already owns.
func TestIndexNamesEveryWireCompletely(t *testing.T) {
	app, _ := mountWorld(t)

	_, raw := do(t, app, http.MethodGet, "/v1/world", "", "", "", nil)
	var got worldIndex
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(got.Wires) == 0 {
		t.Fatal("no wires at all — the index exists to name them")
	}

	seen := map[string]bool{}
	for _, w := range got.Wires {
		if w.Name == "" || w.Path == "" || w.Protocol == "" || w.Auth == "" {
			t.Errorf("incomplete wire %+v — name, path, protocol and auth are all required", w)
		}
		if seen[w.Name] {
			t.Errorf("wire %q listed twice", w.Name)
		}
		seen[w.Name] = true
		if !strings.HasPrefix(w.Path, "/v1/world") {
			t.Errorf("wire %q points at %q, outside this product's prefix", w.Name, w.Path)
		}
	}
	// The two wires the document cannot declare are the whole reason this op
	// exists. If either stops being named here, it becomes undiscoverable again.
	for _, want := range []string{"rest", "mcp", "zap"} {
		if !seen[want] {
			t.Errorf("wire %q is not named — /v1/world/mcp and /v1/world/zap are served by "+
				"world-gw and appear in NO generated document, so this op is the only place "+
				"a caller can find them", want)
		}
	}
}

// TestIndexDoesNotRestateTheOperationList pins the boundary that keeps this op from
// becoming a second, drifting copy of the document. The REST wire points at
// /v1/openapi.json; it must not carry the operations themselves.
func TestIndexDoesNotRestateTheOperationList(t *testing.T) {
	app, _ := mountWorld(t)

	_, raw := do(t, app, http.MethodGet, "/v1/world", "", "", "", nil)
	var got worldIndex
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	for _, w := range got.Wires {
		if w.Name != "rest" {
			continue
		}
		if w.Spec == "" {
			t.Fatal("the rest wire names no spec — it must point at the generated document " +
				"rather than list operations here")
		}
		return
	}
	t.Fatal("no rest wire")
}
