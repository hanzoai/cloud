package reference

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes "every route is a typed op" a GATE instead of a paragraph.
// Prose cannot fail: a route added tomorrow as a raw func(*zip.Ctx) error would
// leave the claim standing and the route invisible to every projection — no
// schema, no description, no MCP tool, no CLI command, no SDK method.

// untypedByDesign is the CLOSED list of operations that are not typed ops. It is
// EMPTY, and that is the claim: this surface has no route that cannot be
// expressed as one.
var untypedByDesign = map[string]string{}

// ops reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry.
func surface(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "reference", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTyped fails when an operation is neither a typed op nor named
// above, so the next route added here is typed by default.
func TestEveryRouteIsTyped(t *testing.T) {
	served, typed := surface(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK method.",
			strings.Join(untyped, ", "))
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED surface, so the prose cannot drift from the binary.
	if len(served) != 6 || len(typed) != 6 {
		t.Errorf("served = %d (want 6), typed = %d (want 6)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := surface(t)
	if len(typed) == 0 {
		t.Fatal("no typed ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/reference/...", key)
		}
	}
}

// TestTheSurfaceIsTheDeclaredPrefix holds that this app answers on exactly one
// address family and takes nothing that belongs to the ml app one prefix over —
// the manifest routes /v1/risk/reference here and /v1/ml/models and /v1/ml/health
// there, and zip refuses two owners for one path at compose time.
func TestTheSurfaceIsTheDeclaredPrefix(t *testing.T) {
	served, _ := surface(t)
	for key := range served {
		_, path, _ := strings.Cut(key, " ")
		if !strings.HasPrefix(path, "/v1/risk/reference") {
			t.Errorf("%s is outside this app's declared prefix", key)
		}
	}
	for _, foreign := range []string{"GET /v1/ml/models", "GET /v1/ml/health"} {
		if served[foreign] {
			t.Errorf("this app serves %s, which belongs to the ml app", foreign)
		}
	}
}
