package admin

// The /v1/admin surface is ONE registry with N projections. These tests pin that:
// every route is a typed op, every op reaches the OpenAPI document and the MCP tool
// list, and every op's prose reaches them from its own doc comment.
//
// They exist because the failure mode is silent. A route added as app.Get still serves
// bytes — it simply vanishes from the document, the tools and the CLI, and nothing
// fails until someone goes looking for it.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// spec mounts the surface and returns the OpenAPI document zip derives from the op
// registry, plus the live route table fiber matches on. The document is read back
// through JSON — which is how every consumer reads it — rather than through zip's
// in-memory Go types, so these tests check what is actually published.
func spec(t *testing.T) (map[string]any, []string) {
	t.Helper()
	app := zip.New(zip.Config{
		Logger:  luxlog.New("test"),
		OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"},
	})
	compose(app)
	routes(app, &cloud.Service[core.State]{State: core.State{}})

	var live []string
	for _, r := range app.Fiber().GetRoutes(true) {
		if strings.HasPrefix(r.Path, "/v1/admin") && r.Method != "HEAD" {
			live = append(live, r.Method+" "+r.Path)
		}
	}

	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("document does not decode: %v", err)
	}
	return doc, live
}

// TestEveryRouteIsATypedOp is the invariant: the document describes the router exactly.
// A route the registry does not know is a route no projection can serve.
func TestEveryRouteIsATypedOp(t *testing.T) {
	doc, live := spec(t)

	described := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			// The document writes {name} where fiber writes :name.
			fiberPath := path
			for seg := range strings.SplitSeq(path, "/") {
				if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
					fiberPath = strings.Replace(fiberPath, seg, ":"+strings.Trim(seg, "{}"), 1)
				}
			}
			described[strings.ToUpper(method)+" "+fiberPath] = true
		}
	}

	for _, r := range live {
		if !described[r] {
			t.Errorf("%s is served but not in the OpenAPI document — it is a raw handler, "+
				"invisible to OpenAPI, MCP and the CLI. Register it with zip.Get[In, Out].", r)
		}
	}
	if len(live) == 0 {
		t.Fatal("no /v1/admin routes registered")
	}
	t.Logf("%d routes, all typed ops", len(live))
}

// TestSchemaRefsResolve guards the one way a typed op can produce a BROKEN document:
// a schema name containing "/" splits into extra JSON-Pointer segments, so its $ref
// addresses nothing. That is what a generic envelope over a package type would emit,
// and why each op declares its own Out.
func TestSchemaRefsResolve(t *testing.T) {
	doc, _ := spec(t)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)

	for name := range schemas {
		if strings.ContainsAny(name, "/~ ") {
			t.Errorf("schema %q cannot be addressed by a $ref: /, ~ and spaces are "+
				"JSON-Pointer syntax, not name characters", name)
		}
	}

	raw, _ := json.Marshal(doc)
	for _, ref := range refsIn(string(raw)) {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if _, ok := schemas[name]; !ok {
			t.Errorf("$ref %q resolves to nothing", ref)
		}
	}
}

// TestDocCommentsReachTheSpec proves the zipdoc pass ran and is current: Go drops
// comments at compile time, so prose in the document can only come from the generated
// zip.Describe calls. An op with no description means the generator has not been re-run
// since the handler was written — run `go generate ./clients/admin/...`.
func TestDocCommentsReachTheSpec(t *testing.T) {
	doc, _ := spec(t)
	for path, item := range doc["paths"].(map[string]any) {
		for method, op := range item.(map[string]any) {
			o := op.(map[string]any)
			if s, _ := o["description"].(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s %s has no description — write a doc comment on the handler "+
					"and run `go generate ./clients/admin/...`", strings.ToUpper(method), path)
			}
			if id, _ := o["operationId"].(string); !strings.HasPrefix(id, "admin") {
				t.Errorf("%s %s: operationId %q should be a stable admin* name — it is what "+
					"the MCP tool and the CLI command are called", strings.ToUpper(method), path, id)
			}
		}
	}
}

// refsIn lists every $ref value in the encoded document.
func refsIn(s string) []string {
	const key = `"$ref":"`
	var out []string
	for i := strings.Index(s, key); i >= 0; i = strings.Index(s, key) {
		s = s[i+len(key):]
		if end := strings.IndexByte(s, '"'); end >= 0 {
			out = append(out, s[:end])
			s = s[end:]
		}
	}
	return out
}
