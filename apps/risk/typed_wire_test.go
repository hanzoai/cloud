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
	"github.com/hanzoai/cloud/manifest"
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
	"GET /v1/risk/health": "a liveness probe, refused by THIS PACKAGE'S own invariant " +
		"rather than by anything about zip. The reason it used to give — 503 carries the degraded " +
		"REPORT as its body, which a typed op's error envelope would drop — has EXPIRED: WithStatus is " +
		"variadic and an answer states which declared status it is, which is how apps/deploy's probe " +
		"became an op. The conversion was written here and TestOps_EveryOpIsAdmittedAndPriced refused " +
		"it: every op must pass o.admit (a per-tenant in-flight slot) and be priced, and a probe must " +
		"do NEITHER — it answers without a tenant, which is what liveness means, and metering it would " +
		"bill a customer for being watched. Exempting it would weaken a bound that stops unbounded " +
		"per-tenant compute, for a tool an agent has no use for.",
}

// TestSurface_IsOnlyUnderRisk is the namespace boundary as a gate.
//
// /v1/ml belongs to a DIFFERENT live product — the Kubernetes model-serving plane
// (apps/ml): /v1/ml/health, /v1/ml/models, /v1/ml/models/{name}/predict, with
// customers on them. This app used to mount its learning leaves inside that
// prefix, which made /v1/ml/models mean "models you serve" and "models that
// learn" at once and put a second owner inside a live product's namespace.
//
// The rule is structural, so it is checked structurally: every operation this app
// serves is under /v1/risk, and nothing it serves is under /v1/ml. A route added
// back into the serving product's prefix fails here rather than in production, on
// somebody else's customers.
func TestSurface_IsOnlyUnderRisk(t *testing.T) {
	served, _, _ := riskOps(t)
	if len(served) == 0 {
		t.Fatal("the app serves nothing at all")
	}
	for key := range served {
		_, path, _ := strings.Cut(key, " ")
		if strings.HasPrefix(path, "/v1/ml") {
			t.Errorf("%s is inside /v1/ml, which the model-SERVING plane owns and is live on — "+
				"a learning op belongs at /v1/risk", key)
		}
		if !strings.HasPrefix(path, "/v1/risk/") {
			t.Errorf("%s is outside /v1/risk — this app has exactly one face", key)
		}
	}
}

// One decision recorded here rather than discovered at integration, because it is
// about the WIRE and is not visible in a handler:
//
// EVERY PRICED OP IS TYPED AND REFUSES IN THE FLEET'S OWN MONEY CONTRACT. A typed
// op's returned error renders as zip's FLAT envelope, which is a second vocabulary
// for a refusal the platform already has words for — so the /v1/risk group carries
// cloud.DenyEnvelope and each gated op returns cloud.Denied, and a 402 from here
// is byte-for-byte a 402 from anywhere else. Held by
// [TestScoreAndLearn_AreGatedOnTheCallersOwnBalance].
//
// This file used to record the opposite decision for score and learn — that they
// were "billed once, at the decision, by the plane that owns the decision". No
// such plane exists: THIS is the plane that owns the decision, and the two ops an
// abuser would call in a loop were free, unbounded compute against per-tenant
// model state and a per-tenant disk write. A note explaining why an expensive op
// is not metered is worth exactly as much as the plane it defers to.

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (mount says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing — and a test that skips it does not
// test a stricter program, it tests a program where every org-scoped op answers
// 403 for a reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountApp mounts risk the way plugin/risk does — the whole Mount, so the
// projection ledgers below read the surface a deployed binary serves and not a
// test-only subset.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	compose(app)
	deps := cloud.Deps{Brand: brandA, DataDir: t.TempDir()}
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use:  %v", err)
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
			"SDK method. Convert it (zip.Get/Post/... on the /v1/risk group), or add it to untypedByDesign "+
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
	if len(typed) != 10 {
		t.Errorf("the model plane publishes %d typed ops, expected 10 "+
			"(score, learn, state, publish, adopt, policy, appetite, features, search, result)", len(typed))
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
			// riskModelState.tenant and riskPublishOut.tenant are OUTPUTS — the server
			// echoing the tenant it resolved, so a reader can see the answer is its
			// own. Only INPUT shapes are gated here.
			if !strings.HasSuffix(name, "In") && name != "riskEvent" && name != "riskRunRef" {
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

// req calls as an ordinary MEMBER of org — the authority every tenant surface
// admits, and the one nearly every op here needs.
func req(t *testing.T, app *zip.App, method, path, org, user, body string) (int, []byte) {
	t.Helper()
	return call(t, app, method, path, org, user, body, false)
}

// reqAdmin calls as an ADMIN OF THAT ORG: the org-scoped, self-service authority
// the identity boundary mints as X-User-IsOrgAdmin, which [admitArming] requires to
// take an organisation's model live.
//
// It is deliberately not SuperAdmin. An organisation arming its own model is its
// own decision, and a test that reached for platform sudo to make it would be
// asserting the wrong rule.
func reqAdmin(t *testing.T, app *zip.App, method, path, org, user, body string) (int, []byte) {
	t.Helper()
	return call(t, app, method, path, org, user, body, true)
}

func call(t *testing.T, app *zip.App, method, path, org, user, body string, orgAdmin bool) (int, []byte) {
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
	if orgAdmin {
		// The bit the identity boundary mints from the caller's role in its OWN org,
		// which is the one principal.IsOrgAdmin reads.
		r.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(r)
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
		{http.MethodPost, "/v1/risk/score", `{"event":{"kind":"account","subject":"u_1"}}`},
		{http.MethodPost, "/v1/risk/learn", `{"events":[{"kind":"account","subject":"u_1"}]}`},
		{http.MethodGet, "/v1/risk/state", ""},
		{http.MethodPut, "/v1/risk/policy", `{"review":0.01,"sample":0.001}`},
		{http.MethodPost, "/v1/risk/state/model", ""},
		{http.MethodPut, "/v1/risk/state/model", `{"address":"not-a-published-address"}`},
		{http.MethodGet, "/v1/risk/policy", ""},
		{http.MethodGet, "/v1/risk/features", ""},
		{http.MethodPost, "/v1/risk/search", `{"days":7}`},
		{http.MethodGet, "/v1/risk/search/srch_1", ""},
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
	code, body := req(t, app, http.MethodGet, "/v1/risk/state", orgA, "u_"+orgA, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/state = %d %s", code, body)
	}
	var st riskModelState
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
	code, body := req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1","nano":1000000000}}`)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/risk/score = %d %s", code, body)
	}
	var out riskScoreOut
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
		code, body := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("POST /v1/risk/learn %s = %d %s, want 400", tc.body, code, body)
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
	for _, k := range []string{"service", "status", "plane", "shelf", "surface", "resident", "evicted", "strained"} {
		if _, ok := rep[k]; !ok {
			t.Errorf("the probe's body has no %q — a probe that does not report is status theatre", k)
		}
	}
	if rep["surface"] != true {
		t.Errorf("the probe reports the surface as %v with the warehouse up", rep["surface"])
	}
}

// TestHealth_ReportsATenantForgettingItsOwnSubjects: a bound that binds must be
// LOUD.
//
// The per-tenant aggregate ceiling degrades that tenant silently by construction:
// at it, its least-recently-active subject is forgotten and every velocity
// feature for that subject then reads as inactive — which is indistinguishable
// from a quiet subject, which is what the model is looking for. Nothing errors,
// nothing 4xxs, and the control is off for that subject.
//
// So the state is NAMED and COUNTED on the probe. A control that switches itself
// off has to be visible from outside the process, and "visible" means a test can
// drive a tenant into it and read it back.
//
// Mutation proof: report `strained` as a constant 0 and this fails.
func TestHealth_ReportsATenantForgettingItsOwnSubjects(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	if code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", ""); code != http.StatusOK {
		t.Fatalf("GET /v1/risk/health = %d %s", code, body)
	} else if strained(t, body) != 0 {
		t.Fatalf("the probe reports a strained tenant before anything has been taught: %s", body)
	}

	// One organisation past its OWN ceiling, which is where it starts forgetting.
	for i := range ringKeyCeiling * 2 {
		body := `{"events":[{"kind":"account","subject":"u_` + itoa(i) + `","nano":1000000}]}`
		if code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, body); code != http.StatusOK {
			t.Fatalf("learn %d = %d %s", i, code, out)
		}
	}

	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/health = %d %s", code, body)
	}
	if got := strained(t, body); got == 0 {
		t.Fatalf("%d subjects went into a %d-subject ceiling and the probe reports %v strained "+
			"tenant(s) — that organisation is forgetting its own subjects and nothing outside the "+
			"process can tell: %s", ringKeyCeiling*2, ringKeyCeiling, got, body)
	}
}

func strained(t *testing.T, body []byte) float64 {
	t.Helper()
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	n, ok := rep["strained"].(float64)
	if !ok {
		t.Fatalf("the probe carries no numeric %q: %s", "strained", body)
	}
	return n
}

// TestManifest_ClaimsNoPrefixOfTheServingPlane is the OTHER half of the namespace
// boundary, and it is the half that decides where production traffic goes.
//
// [TestSurface_IsOnlyUnderRisk] reads the routes this app REGISTERS. The manifest
// row decides which binary a request REACHES: it is longest-prefix routing over
// manifest.Apps, so a row here claiming a leaf of /v1/ml would take that path
// away from the live model-serving plane — /v1/ml/models, /v1/ml/models/{name}/
// predict, and customers on them — and hand it to a binary that serves nothing
// at that path. A 404 on somebody else's live product, from a row in a table.
//
// The two halves are separate because they fail separately: a route can be
// unregistered and still routed, and routed and still unregistered.
func TestManifest_ClaimsNoPrefixOfTheServingPlane(t *testing.T) {
	var mine, serving []string
	for _, a := range manifest.Apps {
		switch a.Name {
		case "risk":
			mine = a.Prefixes
		case "ml":
			serving = a.Prefixes
		}
	}
	if len(mine) == 0 {
		t.Fatal("the risk app claims no prefix at all, so nothing routes to it")
	}
	for _, p := range mine {
		if !strings.HasPrefix(p, "/v1/risk") {
			t.Errorf("the risk app claims %q — this app has exactly one face and it is /v1/risk", p)
		}
	}
	// And the serving plane keeps everything it had. Its own leaves are the live
	// product; this list is what production answers on today.
	for _, want := range []string{"/v1/ml/health", "/v1/ml/models"} {
		var held bool
		for _, p := range serving {
			if p == want {
				held = true
			}
		}
		if !held {
			t.Errorf("the model-SERVING plane no longer claims %q — that path is live with customers "+
				"on it and this track must not have moved it", want)
		}
	}
}
