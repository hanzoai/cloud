package author

// typed_wire_test.go is the route oracle for this surface: every published
// operation must carry a typed registry entry, and every entry must carry prose.
//
// The behaviour suite beside it already drives the wire — the conditional 201s, the
// admin envelope, the two-shape reads, the tenant and SuperAdmin gates — so this
// file asserts only what that suite structurally cannot: that the ROUTER and the
// REGISTRY agree, which is the property a typed conversion exists to establish.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// authorsOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. Reading the router (not the source) is what makes this a gate rather than
// prose — a route added anywhere in routes() shows up here.
func authorsOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, _, _, _ := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "author", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/author") || strings.HasPrefix(p, "/v1/admin/author")
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
	return served, typed
}

// TestEveryAuthorsRouteIsTyped fails when any operation on this surface lacks a
// registry entry. There is no exemption list: nothing here has a wire that typing
// would move, so the next route added is typed or it is red.
func TestEveryAuthorsRouteIsTyped(t *testing.T) {
	served, typed := authorsOps(t)
	if len(served) == 0 {
		t.Fatal("the router serves no authors routes")
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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and "+
			"no SDK method.", strings.Join(untyped, ", "))
	}
}

// TestEveryTypedAuthorsOpIsDescribed fails when a typed op reaches the document with
// no prose. zipdoc lifts the handler's doc comment into BOTH the OpenAPI description
// and the MCP tool description, so an op whose comment does not lift is an SDK method
// and an agent tool with nothing to read.
func TestEveryTypedAuthorsOpIsDescribed(t *testing.T) {
	_, typed := authorsOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed authors ops — the conversion is not wired")
	}
	var bare []string
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			bare = append(bare, key)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("typed op(s) with no description: %s\n"+
			"Run: go generate -run zipdoc ./apps/authors/...", strings.Join(bare, ", "))
	}
}

// TestAdminOpsRefuseANonAdmin pins the gate the typed conversion had to carry
// through a signature that cannot see a header: platform-ness is X-User-IsAdmin,
// which only the identity boundary mints, so every admin op reaches the REQUEST for
// it. A validated tenant who is NOT a SuperAdmin must be refused on all six —
// missing that on one op would hand a tenant the whole book, every org's row
// included.
func TestAdminOpsRefuseANonAdmin(t *testing.T) {
	app, _, _, _ := mount(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/author"},
		{http.MethodPost, "/v1/admin/author/sweep"},
		{http.MethodPost, "/v1/admin/author/aut_x/approve"},
		{http.MethodPost, "/v1/admin/author/aut_x/suspend"},
		{http.MethodPost, "/v1/admin/author/aut_x/payout"},
		{http.MethodGet, "/v1/admin/author/aut_x/basis"},
	} {
		// A VALIDATED tenant, but not an admin.
		if code, _ := req(t, app, tc.method, tc.path, "acme", false, nil); code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin tenant = %d, want 403", tc.method, tc.path, code)
		}
		// And with no principal at all.
		if code, _ := req(t, app, tc.method, tc.path, "", false, nil); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// TestTenantOpsRefuseAnUnvalidatedCaller pins the other half: the four tenant ops
// read their org from principal.OrgFrom — the value cloud.Bridge parks from the
// validated bearer claim — and must refuse a caller who has none rather than
// operating on an empty org.
func TestTenantOpsRefuseAnUnvalidatedCaller(t *testing.T) {
	app, _, _, _ := mount(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/author"},
		{http.MethodGet, "/v1/author/basis"},
		{http.MethodPost, "/v1/author/connect"},
		{http.MethodPost, "/v1/author/repos/verify"},
		{http.MethodPost, "/v1/author/deploys/record"},
	} {
		if code, _ := req(t, app, tc.method, tc.path, "", false, map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// TestBasisRefusesAMalformedPeriod pins the query binding: ?period is validated
// against the ONE shape the accrual latch mints, because the value is echoed back
// AND used as a SQL filter. Through a typed op the value arrives by bindURL, which
// does not validate — so the op must, exactly as periodOf did.
func TestBasisRefusesAMalformedPeriod(t *testing.T) {
	app, _, _, _ := mount(t)
	if code, _ := req(t, app, http.MethodGet, "/v1/author/basis?period=july", "acme", false, nil); code != http.StatusBadRequest {
		t.Errorf("?period=july = %d, want 400", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/author/basis?period=2026-07", "acme", false, nil); code != http.StatusOK {
		t.Errorf("?period=2026-07 = %d, want 200", code)
	}
}
