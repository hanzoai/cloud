package zt

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of zero-trust operations that are NOT typed
// ops. It is EMPTY: all four routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

// ztOpsUnderTest reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router (not the source) is what makes this a gate
// rather than prose — a route added anywhere in routes() shows up here.
func ztOpsUnderTest(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t, &fakeZT{})
	doc, err := openapi.Spec(app, openapi.Info{Title: "zero-trust", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/networks") ||
			strings.HasPrefix(p, "/v1/mesh") || strings.HasPrefix(p, "/v1/edge")
	}
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

// TestEveryRouteIsTypedOrNamed fails when a zero-trust operation is neither a
// typed op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := ztOpsUnderTest(t)
	if len(served) != 4 {
		t.Errorf("zero-trust serves %d operations, expected 4 — update this gate deliberately", len(served))
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
			t.Errorf("untypedByDesign names %q, which zero-trust no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := ztOpsUnderTest(t)
	if len(typed) == 0 {
		t.Fatal("no typed zero-trust ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/zt/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := ztOpsUnderTest(t)
	if len(schemas) == 0 {
		t.Fatal("no zero-trust schemas in the typed registry at all")
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

// TestBridgeIsInstalledOnEveryPrefix is the assertion the conversion turns on, and
// the latent defect it closed. A typed op reads its tenant from the context, which
// only cloud.Bridge parks there; without it every op here would see NO org and
// answer 403 to a perfectly valid request. This subsystem answers on THREE prefixes
// that share no common root, so one Bridge is not enough — each prefix needs its
// own, and this proves all three run by asserting that a validated caller is
// SERVED on every route while an anonymous one is refused.
func TestBridgeIsInstalledOnEveryPrefix(t *testing.T) {
	f := &fakeZT{
		services: []map[string]any{{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}}},
		routers:  []map[string]any{{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme"}, "isOnline": true}},
	}
	app := mountApp(t, f)

	for _, path := range []string{
		"/v1/networks", "/v1/networks/org-acme", "/v1/mesh/services", "/v1/edge/nodes",
	} {
		if code, body := do(t, app, http.MethodGet, path, "acme"); code != http.StatusOK {
			t.Errorf("GET %s as a validated caller = %d (%s), want 200 — is cloud.Bridge installed on this prefix?",
				path, code, body)
		}
	}
	// The two routes that do NOT degrade must still refuse an anonymous caller —
	// Bridge parks what it has and continues; it is the handler that refuses.
	for _, path := range []string{"/v1/mesh/services", "/v1/networks/org-acme"} {
		if code, _ := do(t, app, http.MethodGet, path, ""); code != http.StatusForbidden {
			t.Errorf("GET %s with no principal = %d, want 403", path, code)
		}
	}
}
