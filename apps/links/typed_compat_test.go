package links

// typed_compat_test.go — the projection gate. Every operation on this surface is a
// TYPED op: a route PLUS a registry entry, the one value the OpenAPI operation,
// the MCP tool, the CLI command and the generated SDK method all come from. This
// file MEASURES that against the live router rather than asserting it in prose,
// so the next route added here is typed by default. The parity half — the
// statuses and shapes each converted route kept — is the rest of this package's
// suite, which drives the same router over HTTP.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// linkOps reads BOTH projections of the live router: what the document says is
// served, and which of those carry a typed registry entry.
func linkOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountLink(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "links", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return p == "/v1/links" || strings.HasPrefix(p, "/v1/links/") }
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

// TestEveryRouteIsTyped fails when a link operation carries no typed registry
// entry — so a route added untyped goes red without anyone remembering to check.
func TestEveryRouteIsTyped(t *testing.T) {
	served, typed := linkOps(t)
	if len(served) == 0 {
		t.Fatal("the document serves no /v1/links operations at all")
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... with the full path).", strings.Join(untyped, ", "))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema:
// it becomes the OpenAPI description AND the MCP tool description a model reads.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := linkOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed link ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/links/...", key)
		}
	}
}
