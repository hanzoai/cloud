package gateway

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/openapi"
)

// This file makes the gateway config plane's typed partition a GATE instead of a
// paragraph. "2 of 2" is prose, and prose cannot fail: a route added tomorrow as a
// raw func(*zip.Ctx) error would leave the claim standing and the route invisible to
// every projection — no schema, no description, no MCP tool, no CLI command, no SDK
// method.

// untypedByDesign is the CLOSED list of gateway operations that are NOT typed ops.
// It is EMPTY, and that is the claim: this surface has no wire fact zip cannot
// express. An entry added here has to carry the wire fact that forced it.
var untypedByDesign = map[string]string{}

// gatewayOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. EVERY served operation counts.
func gatewayOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, _ := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "gateway", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a gateway operation is neither a typed op
// nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := gatewayOps(t)

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
			t.Errorf("untypedByDesign names %q, which gateway no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 2 || len(typed) != 2 {
		t.Errorf("served = %d (want 2), typed = %d (want 2)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := gatewayOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed gateway ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/gateway/...", key)
		}
	}
}

// TestOrgOverrideStaysOnTheURL is the reason apps/gateway is in cloud's
// allowedRequestUses rather than naming ?org= on its input. zip binds an input
// field from the BODY as well as the URL, so an `Org` field would start accepting a
// TENANT KEY in the PUT body — a cross-tenant write the caller asserted for itself,
// on a route that has never taken an org there. This pins that it still does not:
// a SuperAdmin body naming another tenant writes the SuperAdmin's own org.
func TestOrgOverrideStaysOnTheURL(t *testing.T) {
	app, st := mountApp(t)
	// An ORG ADMIN of "acme" whose body tries to name "victim" as the target. Not a
	// SuperAdmin, deliberately: the admin org's own row IS the platform row in this
	// store, so a SuperAdmin write would land there and prove nothing about tenants.
	code, body := call(t, app, http.MethodPut, "/v1/gateway/config",
		`{"org":"victim","org_rpm":99}`, orgAdmin("acme"))
	if code != 200 {
		t.Fatalf("write: %d (%s)", code, body)
	}
	if raw, ok, _ := st.Get(t.Context(), "victim"); ok && raw.OrgRPM != 0 {
		t.Fatalf("a body field wrote another tenant's row (org_rpm %d) — the org must come from the "+
			"validated principal or ?org=, never from the body", raw.OrgRPM)
	}
	if got := st.OrgRPM("acme"); got != 99 {
		t.Fatalf("the caller's own org ceiling = %d, want 99", got)
	}
	// And the SuperAdmin's real override, on the URL, still works.
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config?org=globex",
		`{"org_rpm":7}`, superAdmin("admin")); code != 200 {
		t.Fatalf("?org= write: %d (%s)", code, body)
	}
	if got := st.OrgRPM("globex"); got != 7 {
		t.Fatalf("?org= targeted ceiling = %d, want 7", got)
	}
}

// TestReadAndWriteAnswerTheSameShape pins what typing declared: BOTH ops answer an
// edge.Policy, so one published schema describes the whole surface. The platform
// branch returns the saved platform row and the per-org branch the effective view —
// two VALUES of one type, which is why a single Out could be declared for a route
// that branches.
func TestReadAndWriteAnswerTheSameShape(t *testing.T) {
	app, _ := mountApp(t)
	// Platform branch (SuperAdmin) — the saved platform row.
	_, plat := call(t, app, http.MethodPut, "/v1/gateway/config", `{"per_ip_rpm":500}`, superAdmin("admin"))
	// Per-org branch — the effective view.
	_, org := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":60}`, orgAdmin("acme"))
	// The read.
	_, read := call(t, app, http.MethodGet, "/v1/gateway/config", "", orgAdmin("acme"))
	for name, raw := range map[string]string{"platform write": plat, "org write": org, "read": read} {
		var p edge.Policy
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("%s does not decode as the declared edge.Policy: %v (%s)", name, err, raw)
		}
	}
}
