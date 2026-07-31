package catalog

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// routePrefix is the ONE address this lens owns.
const routePrefix = "/v1/catalog"

// TestPagingAndForkableStayStrings pins the tolerance the query-string reader had
// and a bool/int-typed In would have lost. zip's bindURL leaves an unparseable
// value at the field's ZERO and reads a bare `?flag` as TRUE, so an int limit
// could not tell `?limit=0` (a page of nothing, which this surface serves) from
// `?limit=abc` (unset → 50), and a bool forkable would turn `?forkable` from
// "unasked" into "true". Both fields stay STRINGS for exactly that reason.
func TestPagingAndForkableStayStrings(t *testing.T) {
	app := mount(t)
	seed(t)

	// limit=0 is a real answer: zero rows, with the total still counted.
	code, body := do(t, app, http.MethodGet, "/v1/catalog?limit=0", "", nil)
	if code != http.StatusOK {
		t.Fatalf("?limit=0 = %d, want 200", code)
	}
	if got := decode(t, body); len(got.Data) != 0 || got.Total != 3 {
		t.Errorf("?limit=0 → %d rows / total %d, want 0 rows and the full total", len(got.Data), got.Total)
	}

	// limit=abc is unset: the default page.
	code, body = do(t, app, http.MethodGet, "/v1/catalog?limit=abc", "", nil)
	if code != http.StatusOK {
		t.Fatalf("?limit=abc = %d, want 200", code)
	}
	if got := decode(t, body); len(got.Data) != 3 {
		t.Errorf("?limit=abc → %d rows, want the default page of 3", len(got.Data))
	}

	// A BARE ?forkable is unasked, not true — the whole corpus comes back.
	code, body = do(t, app, http.MethodGet, "/v1/catalog?forkable", "", nil)
	if code != http.StatusOK {
		t.Fatalf("?forkable = %d, want 200", code)
	}
	if got := decode(t, body); len(got.Data) != 3 {
		t.Errorf("bare ?forkable → %d rows, want all 3 — an empty value is unasked here, "+
			"not the `flag present means true` convention zip's binder applies", len(got.Data))
	}
}

// catalogOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router rather than the source is what makes the two
// tests below gates instead of prose.
func catalogOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "catalog", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return p == routePrefix || strings.HasPrefix(p, routePrefix+"/") }
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a catalog operation is not a typed op —
// so the next route added here is typed by default. There is no named exemption
// list: this surface is one read, and it types cleanly.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := catalogOps(t)
	if len(served) == 0 {
		t.Fatal("the live router serves no /v1/catalog operation at all")
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method.", strings.Join(untyped, ", "))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema: it
// becomes the OpenAPI description AND the MCP tool description a model reads to
// pick the tool. zipdoc_gen.go carries it into the binary, so an op added without
// regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := catalogOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed catalog ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/catalog/...", key)
		}
	}
}
