package x402

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// routePrefix is the address this subsystem owns.
const routePrefix = "/v1/x402"

// x402Ops reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router rather than the source is what makes the two
// tests below gates instead of prose.
func x402Ops(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	h := newHarness(t)
	doc, err := openapi.Spec(h.app, openapi.Info{Title: "x402", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(h.app)
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

// TestEveryRouteIsTypedOrNamed fails when an x402 operation is not a typed op —
// so the next route added on this prefix is typed by default. There is no named
// exemption list: the surface is one read, and it types cleanly. (Enforce is
// MIDDLEWARE, not a route: it hangs on somebody else's priced group, so it is not
// an operation of this subsystem and has nothing to declare here.)
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := x402Ops(t)
	if len(served) == 0 {
		t.Fatal("the live router serves no /v1/x402 operation at all")
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
	_, typed := x402Ops(t)
	if len(typed) == 0 {
		t.Fatal("no typed x402 ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/x402/...", key)
		}
	}
}

// TestReceiptLookupNeedsAPayer pins the fail-closed half of payerOf. The receipt
// is scoped to the org whose LEDGER was debited, and a request with no validated
// principal has none — so it is refused outright rather than reading with an
// empty scope, which the store would treat as "no org filter".
func TestReceiptLookupNeedsAPayer(t *testing.T) {
	h := newHarness(t)
	code, _, _ := h.req("GET", "/v1/x402/settlements/x402_anything", "", "", "")
	if code != 403 {
		t.Fatalf("anonymous receipt lookup = %d, want 403", code)
	}
}
