package network

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of network operations that are NOT typed
// ops. It is EMPTY: all four routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

// opsUnderTest reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router (not the source) is what makes this a gate
// rather than prose — a route added anywhere in routes() shows up here.
func opsUnderTest(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t, &fakeZT{})
	doc, err := openapi.Spec(app, openapi.Info{Title: "network", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	// One prefix, because there is one capability: every route this app serves is
	// under /v1/network. A second stem here would be the defect the rename closed.
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/network") }
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

// TestEveryRouteIsTypedOrNamed fails when a network operation is neither a
// typed op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := opsUnderTest(t)
	if len(served) != 8 {
		t.Errorf("network serves %d operations, expected 8 — update this gate deliberately", len(served))
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
			t.Errorf("untypedByDesign names %q, which network no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := opsUnderTest(t)
	if len(typed) == 0 {
		t.Fatal("no typed network ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/network/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := opsUnderTest(t)
	if len(schemas) == 0 {
		t.Fatal("no network schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s", strings.Join(bare, ", "))
	}
}

// TestValidatedOrgReachesEveryPrefix is the tenant-boundary assertion. A typed op
// reads its tenant from the context, which only cloud.Bridge parks there — and the
// composer installs that once at the root (compose in these tests, serve.go in
// production), so it covers every prefix this subsystem answers on. This proves a
// validated caller is SERVED on every route while an anonymous one is refused.
func TestValidatedOrgReachesEveryPrefix(t *testing.T) {
	f := &fakeZT{
		services: []map[string]any{{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}}},
		routers:  []map[string]any{{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme"}, "isOnline": true}},
	}
	app := mountApp(t, f)

	for _, path := range []string{
		"/v1/network", "/v1/network/routers", "/v1/network/org-acme", "/v1/network/services",
	} {
		if code, body := do(t, app, http.MethodGet, path, "acme"); code != http.StatusOK {
			t.Errorf("GET %s as a validated caller = %d (%s), want 200 — did the root cloud.Bridge park the org?",
				path, code, body)
		}
	}
	// The two routes that do NOT degrade must still refuse an anonymous caller —
	// Bridge parks what it has and continues; it is the handler that refuses.
	for _, path := range []string{"/v1/network/services", "/v1/network/org-acme"} {
		if code, _ := do(t, app, http.MethodGet, path, ""); code != http.StatusForbidden {
			t.Errorf("GET %s with no principal = %d, want 403", path, code)
		}
	}
}
