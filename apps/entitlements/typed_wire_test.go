package entitlements

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
)

// This file makes the entitlements surface's typed partition a GATE instead of a
// paragraph. "3 of 3" is prose, and prose cannot fail: a route added tomorrow as a
// raw func(*zip.Ctx) error would leave the claim standing and the route invisible
// to every projection — no schema, no description, no MCP tool, no CLI command, no
// SDK method.

// untypedByDesign is the CLOSED list of entitlements operations that are NOT typed
// ops. It is EMPTY, and that is the claim: this surface has no wire fact zip cannot
// express. An entry added here has to carry the wire fact that forced it.
var untypedByDesign = map[string]string{}

// entitlementOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts.
func entitlementOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, _ := mount(t, newFakeCommerce())
	doc, err := openapi.Spec(app, openapi.Info{Title: "entitlements", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an entitlements operation is neither a
// typed op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := entitlementOps(t)

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
			"method. Convert it, or add it to untypedByDesign with the reason typing it would move the wire.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which entitlements no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 3 || len(typed) != 3 {
		t.Errorf("served = %d (want 3), typed = %d (want 3)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := entitlementOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed entitlements ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/entitlements/...", key)
		}
	}
}

// TestDeclaredPrefixesCoverEveryServedRoute is the gate on the latent defect this
// pass fixed. This subsystem owns TWO top-level nouns, and the /v1/<name> default
// MountPrefixes falls back to covers only ONE of them — so before the fix the
// standalone binary attributed half its surface to no subsystem, and any middleware
// the subsystem installed through its own router landed on /v1/entitlements alone
// and never ran for /v1/orgs/…. The apps/plan defect, in its partial form.
//
// It reads the MANIFEST — the same list plugin/entitlements/main.go passes — against
// the routes the REAL mount serves, so adding a route outside both prefixes goes red
// here rather than silently ungated in production.
func TestDeclaredPrefixesCoverEveryServedRoute(t *testing.T) {
	served, _ := entitlementOps(t)
	prefixes := manifest.PrefixesFor("entitlements")
	if len(prefixes) == 0 {
		t.Fatal("the manifest declares no prefixes for entitlements")
	}
	// The document writes a path param as {org}; a declared prefix writes it as :org.
	// Compare on the SHAPE, which is what fiber matches on.
	seg := func(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }
	covers := func(prefix, path string) bool {
		ps, xs := seg(prefix), seg(path)
		if len(ps) > len(xs) {
			return false
		}
		for i, p := range ps {
			if strings.HasPrefix(p, ":") || strings.HasPrefix(xs[i], "{") {
				continue // a param segment matches whatever sits there
			}
			if p != xs[i] {
				return false
			}
		}
		return true
	}
	for key := range served {
		path := key[strings.Index(key, " ")+1:]
		found := false
		for _, p := range prefixes {
			if covers(p, path) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is served but lies outside every declared prefix %v — cloud.Declare cannot "+
				"attribute it, and the subsystem's own middleware will not run for it. "+
				"Add the prefix to manifest/apps.go.", key, prefixes)
		}
	}
	// And the default the framework would fall back to must NOT be enough, so the
	// reason plugin/entitlements/main.go passes the manifest list stays visible.
	def := "/v1/entitlements"
	uncovered := 0
	for key := range served {
		if !covers(def, key[strings.Index(key, " ")+1:]) {
			uncovered++
		}
	}
	if uncovered == 0 {
		t.Error("every route now fits the /v1/<name> default — the explicit Prefixes in " +
			"plugin/entitlements/main.go can be dropped, and this test with it")
	}
}

// TestTheOrgComesFromTheURLNotTheBody pins what `json:"-" url:"org"` buys. The org
// path segment is an ADDRESS the gate re-checks against the validated principal, and
// a body naming a different org must not be able to redirect the write — nor appear
// as a body property a caller could believe in.
func TestTheOrgComesFromTheURLNotTheBody(t *testing.T) {
	app, s := mount(t, newFakeCommerce())
	// An org member of acme, whose body claims to be operating on "victim".
	code, body := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme",
		map[string]any{"org": "victim", "remove": []string{"engine"}}))
	if code != http.StatusOK {
		t.Fatalf("mutate: %d (%s)", code, body)
	}
	if got, err := s.store.List(t.Context(), "victim"); err != nil || len(got) != 0 {
		t.Fatalf("victim's row was touched (%v, err %v) — the org is the URL's, never the body's", got, err)
	}
	// A cross-org attempt through the URL is still the 403 it always was.
	if code, _ := send(t, app, orgMember("GET", "/v1/orgs/victim/entitlements", "acme", nil)); code != http.StatusForbidden {
		t.Fatalf("cross-org read: %d, want 403", code)
	}
}
