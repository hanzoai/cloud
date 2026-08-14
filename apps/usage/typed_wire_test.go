package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// typed_wire_test.go MEASURES the facts typing /v1/usage could have moved. Three of
// them would have been invisible to a status-code test: the one-or-many report
// body, the no-store header on the two money reads, and that the subject the
// account board is scoped by still comes from the VALIDATED principal and never
// from an input field.

// TestReportTakesOneSampleOrMany proves the collector's wire is unchanged. The
// single-sample fields are SPELLED OUT on reportReq rather than embedded, because
// zip's schema walk skips an embedded unexported type — the wire would have been
// identical and the document would have described none of it. This asserts the two
// shapes still reach the same place.
func TestReportTakesOneSampleOrMany(t *testing.T) {
	one := reportReq{Provider: "anthropic", Machine: "gb10", Window: "day", UsedPct: 12}
	if got := one.samplesOf(); len(got) != 1 || got[0].Provider != "anthropic" ||
		got[0].Machine != "gb10" || got[0].Window != "day" || got[0].UsedPct != 12 {
		t.Fatalf("a top-level sample must reach the batch whole: %+v", got)
	}

	many := reportReq{
		Provider: "ignored-when-samples-present",
		Samples: []sampleReq{
			{Provider: "anthropic", Machine: "a", Window: "day"},
			{Provider: "openai", Machine: "b", Window: "week"},
		},
	}
	if got := many.samplesOf(); len(got) != 2 || got[0].Provider != "anthropic" || got[1].Provider != "openai" {
		t.Fatalf("samples[] must win over the top-level fields: %+v", got)
	}

	if got := (reportReq{}).samplesOf(); got != nil {
		t.Fatalf("an empty body must yield no samples (the 400), got %+v", got)
	}
}

// TestEverySingleSampleFieldSurvives pins the projection between the top-level wire
// fields and the one-sample form. A field added to sampleReq and forgotten here
// would silently stop arriving for callers that use the single-sample shape.
func TestEverySingleSampleFieldSurvives(t *testing.T) {
	in := reportReq{
		Provider: "p", Account: "a", Plan: "pl", Kind: "subscription", Machine: "m",
		Lane: "l", Window: "day", WindowMinutes: 5, WindowStart: "ws", ResetsAt: "ra",
		UsedPct: 1.5, Confidence: "high", Synthetic: true,
		Requests: 1, InputTokens: 2, OutputTokens: 3, TotalTokens: 4, CachedInputTokens: 5,
		CostCents: 6, CostLimitCents: 7, Currency: "usd",
	}
	want := sampleReq{
		Provider: "p", Account: "a", Plan: "pl", Kind: "subscription", Machine: "m",
		Lane: "l", Window: "day", WindowMinutes: 5, WindowStart: "ws", ResetsAt: "ra",
		UsedPct: 1.5, Confidence: "high", Synthetic: true,
		Requests: 1, InputTokens: 2, OutputTokens: 3, TotalTokens: 4, CachedInputTokens: 5,
		CostCents: 6, CostLimitCents: 7, Currency: "usd",
	}
	if got := in.single(); got != want {
		t.Fatalf("single() dropped or renamed a field:\n got %+v\nwant %+v", got, want)
	}
}

// TestMoneyReadsStayNoStore pins the header a typed op's signature drops. Per-tenant
// money must never be cached by a browser or an intermediary, and Cache-Control is a
// RESPONSE header only the request reaches — so noStore writes it through the bridge.
func TestMoneyReadsStayNoStore(t *testing.T) {
	app := mountApp(t)
	for _, path := range []string{"/v1/usage/summary", "/v1/usage/analytics?plan=pro"} {
		hdr := headerOf(t, app, path, "alice", "acme", "Cache-Control")
		// analytics is entitlement-gated and may 402; the header only has to be
		// present on the answers that carry tenant data.
		if code := statusOf(t, app, path, "alice", "acme"); code == http.StatusOK && hdr != "no-store" {
			t.Errorf("%s answered 200 with Cache-Control=%q, want no-store", path, hdr)
		}
	}
}

// TestSubjectIsNeverAnInputField is the reason caller() reaches the request. The
// account board is scoped to the caller's OWN linked accounts, so it needs the
// validated user id as well as the tenant — and neither may be an input field,
// because an input field is caller-supplied. A request with an org header but NO
// validated principal is refused, which is the fail-closed half of the same fact.
func TestSubjectIsNeverAnInputField(t *testing.T) {
	app := mountApp(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/usage/summary"},
		{http.MethodGet, "/v1/usage/samples?provider=anthropic"},
		{http.MethodGet, "/v1/usage/analytics"},
		{http.MethodPost, "/v1/usage"},
	} {
		if code, raw := drive(t, app, c.method, c.path, "acme", "", nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s with an org header and no validated principal: want 401, got %d (%s)",
				c.method, c.path, code, raw)
		}
	}

	// And the scope that IS served is the principal's, echoed back — never a value
	// the caller could have named.
	code, raw := drive(t, app, http.MethodGet, "/v1/usage/summary?user=stranger&org=other", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("summary: want 200, got %d (%s)", code, raw)
	}
	var got usageSummary
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Scope.Org != "acme" || got.Scope.User != "alice" {
		t.Fatalf("a query redirected the scope: %+v", got.Scope)
	}
}

// TestAnalyticsAccessNeedsNoPrincipal pins that the entitlement echo stayed
// UNGATED: it reports a catalog contract and carries no tenant data, and typing it
// must not have added an identity requirement.
func TestAnalyticsAccessNeedsNoPrincipal(t *testing.T) {
	app := mountApp(t)
	code, raw := drive(t, app, http.MethodGet, "/v1/usage/analytics/access", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("analytics access with no principal: want 200, got %d (%s)", code, raw)
	}
	var got usageAnalyticsAccess
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Plan != "" {
		t.Fatalf("an empty plan must echo empty, got %q", got.Plan)
	}
}

// headerOf drives one GET and returns a response header.
func headerOf(t *testing.T, app *zip.App, path, user, org, name string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Test(req, zip.TestConfig{Timeout: usageTestTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.Header.Get(name)
}

// statusOf drives one GET and returns its status.
func statusOf(t *testing.T, app *zip.App, path, user, org string) int {
	t.Helper()
	code, _ := drive(t, app, http.MethodGet, path, org, user, nil)
	return code
}

// ── the projection gate ─────────────────────────────────────────────────────
//
// Both halves of this package's surface are MEASURED here rather than asserted in
// prose, because prose cannot go red: a route added untyped goes red without anyone
// remembering to name it, a reason naming a route this package no longer serves goes
// red too, and the two ledgers must sum to what the live router actually serves.

// untypedByDesign is the CLOSED list of operations here that are NOT typed ops, each
// with the wire fact that keeps it out. A typed op is a route PLUS a registry entry —
// the one value the OpenAPI operation, the MCP tool, the CLI command and the generated
// SDK method all come from — so an operation missing from that registry is invisible to
// all four. Addresses are written the way the DOCUMENT writes them.
var untypedByDesign = map[string]string{
	"POST /v1/usage/openrouter": "the OpenRouter Broadcast door. Its credential is a HEADER — a " +
		"publishable ingest key, because Broadcast signs nothing and its only authentication is the " +
		"Headers map it sends verbatim — and a typed op holds a CONTEXT, not a request, so it cannot " +
		"read one (apps/principal). Every inbound webhook in this repo is raw for that reason. It " +
		"DECLARES the operation through openapi.Register and openapi.Describe, with both bodies " +
		"free-form and their shape in the prose — the request is OpenTelemetry's OTLP/JSON and this " +
		"package decodes only the subset a usage row needs, so publishing that subset as the schema " +
		"would document a wire nobody sends. The cost of staying untyped is the MCP tool and the SDK " +
		"method, never the document.",
}

// usageOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the REAL mount, not a reconstruction of it, is what makes this a gate.
func usageOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "usage", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op nor
// one named above — so the next route added is typed by default, and dropping one out
// of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := usageOps(t)

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
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which usage no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool. zipdoc_gen.go carries it into the binary,
// so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := usageOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed usage ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/usage/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate cannot
// see. A typed op publishes its Out's whole schema, and a property that reaches
// openapi.yaml with no description reaches every generated SDK and every MCP inputSchema
// without one too.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "usage", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: only the marshalled form is what an
	// SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/usage/...",
			len(bare), strings.Join(bare, ", "))
	}
}
