package openapi_test

// DESCRIBED IS NOT DISPATCHABLE, and x-tool is the difference.
//
// The fleet's agent door reads a catalog built from these documents and offers
// every operation in it as a callable name. A document carries every ROUTE; a
// child answers only for its TYPED ops, because that is the set zip walks to
// build MCP tools. So an operation that reaches the catalog without being typed
// is a name the door advertises and the child rejects — measured on the live
// door as `unknown tool`, across 157 operations.
//
// This asserts the three ways an operation can reach a document and that only
// one of them is dispatchable. Without it the mark is a boolean nobody checks,
// and the catalog goes back to carrying descriptions as if they were capability.

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type toolIn struct {
	Say string `json:"say"`
}

type toolOut struct {
	Heard string `json:"heard"`
}

// declaredIn is the body an untyped route DECLARES through openapi.Register —
// the seam that gives an untyped route a schema and still no tool.
type declaredIn struct {
	Note string `json:"note"`
}

// Register is safe to call from a test init — a declaration renders only on a
// live route, so it is inert in every sibling that does not serve this path.
// openapi.Describe is NOT: prose is judged for orphans across the whole package
// (openapi.Complete), so declaring it here fails two unrelated tests. The prose
// half of this case is therefore asserted by the packages that actually carry it
// (apps/s3 describes untyped routes) rather than manufactured globally here.
func init() {
	openapi.Register("/v1/probe/declared", http.MethodPost, declaredIn{}, toolOut{})
}

func toolApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{Logger: luxlog.New("tool"), DisableStartupMessage: true})

	// TYPED: in the registry, therefore an MCP tool, therefore dispatchable.
	zip.Post(a, "/v1/probe/typed", func(context.Context, *toolIn) (*toolOut, error) {
		return &toolOut{}, nil
	})
	// UNTYPED, with prose. The document describes it; the child has never heard
	// of its name.
	a.Get("/v1/probe/described", func(c *zip.Ctx) error { return c.JSON(200, toolOut{}) })
	// UNTYPED, with a DECLARED body. This is the one that looks typed from the
	// document — it carries a requestBody and a response schema — which is why
	// the catalog could not tell them apart by reading the JSON.
	a.Post("/v1/probe/declared", func(c *zip.Ctx) error { return c.JSON(200, toolOut{}) })
	return a
}

// TestOnlyATypedOpIsMarkedDispatchable: the mark tracks the registry, not the
// richness of the description.
func TestOnlyATypedOpIsMarkedDispatchable(t *testing.T) {
	doc, err := openapi.Spec(toolApp(t), openapi.Info{Title: "Hanzo Cloud", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method string
		want         bool
	}{
		{"/v1/probe/typed", "post", true},
		{"/v1/probe/described", "get", false},
		{"/v1/probe/declared", "post", false},
	} {
		op, ok := doc.Paths[tc.path][tc.method]
		if !ok {
			t.Fatalf("%s %s is not in the document at all", tc.method, tc.path)
		}
		if op.Tool != tc.want {
			t.Errorf("%s %s: x-tool=%v, want %v — the mark must follow the typed registry, "+
				"which is the set zip builds MCP tools from", tc.method, tc.path, op.Tool, tc.want)
		}
	}
}

// TestTheDeclaredRouteLooksTypedAndIsNot is the reason the mark exists rather
// than a JSON heuristic. Reading the document for "has a requestBody" or "has a
// description" classifies this route as dispatchable and it is not, which is
// exactly how the catalog came to advertise names its children reject.
func TestTheDeclaredRouteLooksTypedAndIsNot(t *testing.T) {
	doc, err := openapi.Spec(toolApp(t), openapi.Info{Title: "Hanzo Cloud", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	declared := doc.Paths["/v1/probe/declared"]["post"]
	if declared.RequestBody == nil {
		t.Fatal("the untyped-but-declared route lost its declared body, so this test no " +
			"longer covers the case it exists for")
	}
	if declared.Tool {
		t.Fatal("a route with a declared body is marked dispatchable — every " +
			"document-shaped heuristic makes this mistake, which is why the mark is written " +
			"where the registry is read")
	}
}
