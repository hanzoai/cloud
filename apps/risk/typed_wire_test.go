package risk

// typed_wire_test.go — this makes "every route is a typed zip op" a GATE instead
// of a paragraph.
//
// Prose cannot fail. A route added tomorrow as a raw func(*zip.Ctx) error would
// leave the claim standing and the route invisible to the OpenAPI document, the
// MCP tool list, the CLI and every generated SDK. Here the claim is a test, so
// the route that falsifies it says so.
//
// It also carries the POSITIVE half of the tenant argument, because a validated
// principal only exists over a real request: an organisation reaches a typed op
// through the identity the gateway minted, never through a field.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of operations that are NOT typed ops, each
// with the WIRE fact that keeps it raw. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. This one is missing on purpose. The address is written
// the way the DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	// The REAL probe. It answers 503 CARRYING THE DEGRADED REPORT as its body
	// (which component failed, and the real error), and that body is the whole
	// point of a probe. A typed op reaches a non-2xx only by returning an error,
	// and zip renders that as the flat {"status","code","error"} envelope, which
	// drops exactly the detail the probe exists to deliver.
	"GET /v1/risk/health": "a REAL probe: 503 carries the degraded REPORT as its body " +
		"(plane/shelf/surface/error), which a typed op's error envelope would drop.",
}

// Two decisions recorded here rather than discovered at integration, because both
// are about the WIRE and neither is visible in a handler:
//
// POST /v1/ml/search IS TYPED AND IS GATED. cloud.DenyResource writes the fleet's
// NESTED {"error":{"code","message"}} 402 in band, and a typed op's returned error
// renders as the FLAT envelope — so typing a gated op changes the refusal body.
// That only matters for a route an existing client already parses, and this is a
// NEW route with no such client. It therefore stays typed and returns a typed
// 402, which is the shape every new surface should use.
//
// POST /v1/ml/score AND /v1/ml/learn ARE NOT METERED HERE. The billable unit is a
// SCREEN, and a screen is billed once, at the decision, by the plane that owns
// the decision. Metering the same physical act twice on two surfaces is a double
// charge no test would catch, so the model plane meters only what is its own: an
// exhaustive search.

// mountApp mounts risk the way plugin/risk does — the whole Mount, so the
// projection ledgers below read the surface a deployed binary serves and not a
// test-only subset.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("risktest"), Brand: brandA, DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// riskOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, so a route mounted at an address
// nobody expected is caught rather than filtered out.
func riskOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "risk", Version: "v1"})
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
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when an operation is neither a typed op nor
// named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := riskOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the /v1/ml group), or add it to untypedByDesign "+
			"with the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which risk no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got := len(typed) + len(untypedByDesign); got != len(served) {
		t.Errorf("%d typed + %d named = %d, but risk serves %d operations",
			len(typed), len(untypedByDesign), got, len(served))
	}
	if len(typed) != 9 {
		t.Errorf("the model plane publishes %d typed ops, expected 9 "+
			"(score, learn, state, appetite, snapshot, restore, features, search, result)", len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := riskOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed risk ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/risk/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half the op-level gate cannot
// see. Typing a route documents its ADDRESS and its SHAPE; it does not document
// the shape's FIELDS.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := riskOps(t)
	if len(schemas) == 0 {
		t.Fatal("no risk schemas in the typed registry at all")
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
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. "+
			"Write a doc comment on the struct field and run: go generate -run zipdoc ./apps/risk/...",
			strings.Join(bare, ", "))
	}
}

// TestNoInputCarriesAnOrganisation is the schema-level statement of the tenant
// rule: no published input shape has a field a caller could use to name an
// organisation. An In field is caller-supplied, so a tenant key read from one is
// a cross-tenant read the caller asserted for itself.
func TestNoInputCarriesAnOrganisation(t *testing.T) {
	_, _, schemas := riskOps(t)
	bad := []string{"org", "organisation", "organization", "tenant", "brand", "owner"}
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, _ := sch["properties"].(map[string]any)
		for field := range props {
			// mlModelState.tenant and mlSnapshotOut.tenant are OUTPUTS — the server
			// echoing the tenant it resolved, so a reader can see the answer is its
			// own. Only INPUT shapes are gated here.
			if !strings.HasSuffix(name, "In") && name != "mlEvent" && name != "mlRunRef" {
				continue
			}
			for _, b := range bad {
				if strings.EqualFold(field, b) {
					t.Errorf("input schema %s carries a %q field — the tenant must come from the validated principal, never the wire", name, field)
				}
			}
		}
	}
}

// ── the wire ─────────────────────────────────────────────────────────────────

func req(t *testing.T, app *zip.App, method, path, org, user, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		r.Header.Set("X-User-Id", user)
	}
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestTypedOpsRefuseAnUnvalidatedPrincipal: an X-Org-Id with no X-User-Id is
// exactly the forged-header case — the header survived the edge but no credential
// minted it. Every op refuses it, and the refusal comes BEFORE anything reaches a
// model or the warehouse.
func TestTypedOpsRefuseAnUnvalidatedPrincipal(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/ml/score", `{"event":{"kind":"account","subject":"u_1"}}`},
		{http.MethodPost, "/v1/ml/learn", `{"events":[{"kind":"account","subject":"u_1"}]}`},
		{http.MethodGet, "/v1/ml/state", ""},
		{http.MethodPut, "/v1/ml/state/appetite", `{"review":0.01,"sample":0.001}`},
		{http.MethodPost, "/v1/ml/state/snapshot", ""},
		{http.MethodPost, "/v1/ml/state/restore", `{"body":{"version":1}}`},
		{http.MethodGet, "/v1/ml/features", ""},
		{http.MethodPost, "/v1/ml/search", `{"days":7}`},
		{http.MethodGet, "/v1/ml/search/srch_1", ""},
	} {
		code, body := req(t, app, tc.method, tc.path, orgA, "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// TestTypedOpsResolveTheTenantFromTheValidatedPrincipal is the POSITIVE half:
// a validated caller reaches its OWN model, and the tenant the server echoes is
// the deployment's brand joined to the caller's organisation — neither of which
// the caller sent.
func TestTypedOpsResolveTheTenantFromTheValidatedPrincipal(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/ml/state", orgA, "u_"+orgA, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/ml/state = %d %s", code, body)
	}
	var st mlModelState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if st.Tenant != brandA+sep+orgA {
		t.Fatalf("the resolved tenant is %q, want %q — the brand comes from the deployment and the org from the principal",
			st.Tenant, brandA+sep+orgA)
	}
	if st.Live {
		t.Fatal("a brand-new model reports LIVE — shadow must be the default")
	}
	if st.Shape == "" {
		t.Fatal("the model reports no shape — an auditor has nothing to pin an alert to")
	}
}

// TestScoreRefusesRatherThanReportingClean: a model that has learned nothing
// declines with a REASON. A score of zero would be indistinguishable from a clean
// result, and that is the single most dangerous answer this surface can give.
func TestScoreRefusesRatherThanReportingClean(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	code, body := req(t, app, http.MethodPost, "/v1/ml/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1","nano":1000000000}}`)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/ml/score = %d %s", code, body)
	}
	var out mlScoreOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if out.Scored {
		t.Fatal("a model that has learned nothing scored an event")
	}
	if out.Refusal == "" {
		t.Fatal("the model declined without saying why — silence must never read as a clean result")
	}
	if out.Alert {
		t.Fatal("a model that declined to score raised an alert")
	}
}

// TestLearnRefusesAnEmptyBatchAndAnUnknownKind: the two validations that keep an
// event from landing on nobody. A subject that names nobody would pool every
// anonymous event in the organisation into one imaginary customer.
func TestLearnRefusesAnEmptyBatchAndAnUnknownKind(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	for _, tc := range []struct{ body string }{
		{`{"events":[]}`},
		{`{"events":[{"kind":"nosuchkind","subject":"u_1"}]}`},
		{`{"events":[{"kind":"account","subject":""}]}`},
	} {
		code, body := req(t, app, http.MethodPost, "/v1/ml/learn", orgA, "u_"+orgA, tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("POST /v1/ml/learn %s = %d %s, want 400", tc.body, code, body)
		}
	}
}

// TestHealthCarriesItsReport: the one untyped route, doing the one thing a typed
// op could not.
func TestHealthCarriesItsReport(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/health = %d %s", code, body)
	}
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	for _, k := range []string{"service", "status", "plane", "shelf", "surface", "resident"} {
		if _, ok := rep[k]; !ok {
			t.Errorf("the probe's body has no %q — a probe that does not report is status theatre", k)
		}
	}
	if rep["surface"] != true {
		t.Errorf("the probe reports the surface as %v with the warehouse up", rep["surface"])
	}
}
