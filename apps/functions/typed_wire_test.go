package functions

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of functions operations that are NOT typed ops.
// It is EMPTY: all sixteen routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

// opsUnderTest reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func opsUnderTest(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "functions", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/function") }
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
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when an functions operation is neither a typed
// op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := opsUnderTest(t)
	if len(served) != 11 {
		t.Errorf("functions serves %d operations, expected 11 — update this gate deliberately", len(served))
	}

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which functions no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := opsUnderTest(t)
	if len(typed) == 0 {
		t.Fatal("no typed functions ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/functions/...", key)
		}
	}
}

// proseless is the CLOSED list of published properties that carry NO description,
// and it names a generator limitation rather than a hole in anyone's diligence.
//
// All sixteen are PROMOTED from an embedded type: functionDetail embeds functionView,
// the schema builder inlines the embedded struct's fields into the outer object, and
// zipdoc keys a field's prose by the type that DECLARES it. So the sentences exist and
// are published — under functionView, which this surface answers with in its own right
// — and only the inlined copy is bare. Unrolling the embedding would fix the lookup and
// change the wire, which a description task may not do.
//
// Exact in BOTH directions. A bare property anywhere else goes red, and an entry here
// that starts publishing prose goes red too — that is the day zipdoc follows an
// embedding, and this ledger shrinks instead of outliving the gap.
var proseless = map[string]bool{
	"functionDetail.avgDurationMs":  true,
	"functionDetail.createdAt":      true,
	"functionDetail.endpoint":       true,
	"functionDetail.envCount":       true,
	"functionDetail.environment":    true,
	"functionDetail.errors7d":       true,
	"functionDetail.image":          true,
	"functionDetail.invocations7d":  true,
	"functionDetail.lastDeployedAt": true,
	"functionDetail.memoryLimit":    true,
	"functionDetail.name":           true,
	"functionDetail.namespace":      true,
	"functionDetail.status":         true,
	"functionDetail.successRate":    true,
	"functionDetail.target":         true,
	"functionDetail.timeoutSec":     true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "functions", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("functions publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/functions describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
