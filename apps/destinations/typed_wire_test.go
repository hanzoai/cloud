package destinations

import (
	"bytes"
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

// This file is the MEASUREMENT that typing /v1/destinations did not move its
// wire, and the CLOSED ledger of the one route that stayed untyped. The refusal
// used to be a sentence in a comment, which is a promise; here it is a gate, so
// a sixth route added untyped goes red and a stale reason naming a route this
// package no longer serves goes red too.

// untypedByDesign is the closed list of operations that are NOT typed ops, each
// with the WIRE FACT that typing it would move. It has exactly one entry.
var untypedByDesign = map[string]string{
	"POST /v1/destinations/{platform}": "connect. Its request body's property NAMES are chosen at " +
		"REQUEST time by the addressed platform's own Spec — destinations.go reads body[f.Key] for each " +
		"of dest.Spec().Fields and body[camelOf(name)] for each of dest.Spec().Secrets, so ga4 takes " +
		"measurement_id and meta takes pixel_id — and no Go struct describes an object whose keys the " +
		"URL picks. Worse, toStr (destinations.go) accepts each of those values as a JSON string, a " +
		"number OR a bool and coerces it to text, precisely so a console may send a numeric pixel id: a " +
		"typed `string` field turns today's accepted {\"pixel_id\": 123} into a 400. It DECLARES both " +
		"bodies through openapi.Register instead, so the cost of staying untyped is prose, an MCP tool " +
		"and a CLI command — not a document claiming it takes no body.",
}

// mountApp mounts the destinations surface on a fresh in-memory app with a temp
// store, exactly as the unified binary does — and, deliberately, with NO app-wide
// cloud.Bridge, so the bridge these ops read their tenant through has to be the
// one Mount installs itself.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv(publicFanoutEnv, "0") // a document is not a reason to install a live sink
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// do issues one request. org=="" is an ANONYMOUS caller; admin marks the caller
// an org admin, which is the header the mutation gate reads and the ONLY way to
// assert it — there is no request field for the org or for admin-ness, by design.
func do(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// destinationOps reads BOTH projections of the LIVE router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router rather than the source is what makes
// this a gate and not prose.
func destinationOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "destinations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/destinations") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
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
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a destinations operation is neither a
// typed op nor one named in untypedByDesign — so the next route added here is
// typed by default, and dropping one out of the registry takes a deliberate edit
// with a reason. The two ledgers must also SUM to the served surface, so a stale
// reason cannot hide behind a route that no longer exists.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := destinationOps(t)
	if len(served) != 5 {
		t.Fatalf("destinations serves %d operations, not the 5 these ledgers know: %s", len(served), sorted(served))
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
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which destinations does not serve — a stale reason nobody can re-check", key)
		}
		if _, isTyped := typed[key]; isTyped {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := destinationOps(t)
	if len(typed) != 4 {
		t.Fatalf("the registry carries %d operations, want 4", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestTheUntypedRouteStillDeclaresItsBodies is the other half of the refusal.
// "No declaration" and "a body with dynamic keys" were rendering IDENTICALLY —
// as an operation with no requestBody — so every SDK generated from the document
// offered a connect call with nowhere to put the credential. openapi.Register
// states both sides; this asserts they arrived.
func TestTheUntypedRouteStillDeclaresItsBodies(t *testing.T) {
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "destinations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	op := doc.Paths["/v1/destinations/{platform}"]["post"]
	if op == nil {
		t.Fatal("POST /v1/destinations/{platform} is not in the document at all")
	}
	if op.RequestBody == nil {
		t.Fatal("the connect route publishes NO request body — the one thing it cannot work without")
	}
	// RequestBody/Responses are open-typed on Operation (Register contributes
	// *Schema, the typed fold contributes zip's JSON verbatim), so read them the
	// way a client generator does: as the JSON they marshal to.
	var body struct {
		Content map[string]struct {
			Schema struct {
				Type                 string          `json:"type"`
				AdditionalProperties json.RawMessage `json:"additionalProperties"`
			} `json:"schema"`
		} `json:"content"`
	}
	raw, err := json.Marshal(op.RequestBody)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	schema := body.Content["application/json"].Schema
	if schema.Type != "object" || len(schema.AdditionalProperties) == 0 {
		t.Fatalf("the connect body is not declared as an open object: %s", raw)
	}
	raw, err = json.Marshal(op.Responses)
	if err != nil {
		t.Fatalf("marshal responses: %v", err)
	}
	var responses map[string]json.RawMessage
	if err := json.Unmarshal(raw, &responses); err != nil {
		t.Fatalf("decode responses: %v", err)
	}
	if _, ok := responses["2XX"]; !ok {
		t.Fatalf("the connect route publishes no success response: %s", raw)
	}
}

// TestTestReportsTheSameTwoShapes pins the one Out whose fields are POINTERS.
// The route reports a platform rejection as DATA at 200, so it answers two
// shapes: {ok,error} on failure and {ok,sent,message} on success. A non-pointer
// field with `omitempty` would drop a real "sent": 0; without it, every failure
// would gain a "sent": 0 the wire has never carried. Both are invisible to a
// status-code test, so the KEYS are what this asserts.
func TestTestReportsTheSameTwoShapes(t *testing.T) {
	app := mountApp(t)
	// A destination that is not connected never reaches the send, so drive the
	// shapes through the Out type directly — the failure branch is what a typed
	// nil pointer has to omit, and the success branch is what it has to keep.
	msg, boom := "queued", "platform refused"
	sent := 0
	for _, tc := range []struct {
		name string
		out  destinationTest
		want []string
	}{
		{"failure", destinationTest{OK: false, Error: &boom}, []string{"error", "ok"}},
		{"success", destinationTest{OK: true, Sent: &sent, Message: &msg}, []string{"message", "ok", "sent"}},
	} {
		b, err := json.Marshal(tc.out)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s branch answers keys %v, want %v", tc.name, keys, tc.want)
		}
	}
	// And the live wire still refuses a disconnected destination the same way.
	if code, _ := do(t, app, http.MethodPost, "/v1/destinations/ga4/test", "acme", true, nil); code != http.StatusNotFound {
		t.Fatalf("test on a disconnected destination want 404, got %d", code)
	}
}

// TestMutationsRequireOrgAdmin pins the gate cloud.Request exists for. The org
// alone is not enough to forget a credential or spend one: org-admin-ness rides
// in X-User-IsOrgAdmin, which principal.OrgFrom does not carry, so an op that
// could not reach the request would have silently dropped this check.
func TestMutationsRequireOrgAdmin(t *testing.T) {
	app := mountApp(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/destinations/ga4"},
		{http.MethodPost, "/v1/destinations/ga4/test"},
	} {
		if code, body := do(t, app, r.method, r.path, "acme", false, nil); code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin got %d, want 403 (%s)", r.method, r.path, code, body)
		}
	}
}

// TestReadsAreOrgScopedAndFailClosed is the tenancy claim: no request field can
// name the tenant, so an anonymous caller reads nothing at all, and an unknown
// platform is a 404 rather than a leak.
func TestReadsAreOrgScopedAndFailClosed(t *testing.T) {
	app := mountApp(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/destinations"},
		{http.MethodGet, "/v1/destinations/ga4"},
		{http.MethodDelete, "/v1/destinations/ga4"},
		{http.MethodPost, "/v1/destinations/ga4/test"},
	} {
		if code, body := do(t, app, r.method, r.path, "", false, nil); code != http.StatusForbidden {
			t.Errorf("%s %s anonymous got %d, want 403 (%s)", r.method, r.path, code, body)
		}
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/destinations/nope", "acme", false, nil); code != http.StatusNotFound {
		t.Error("an unknown platform is not a 404")
	}
}

// TestListIsOneKeyedList pins the browse envelope. The op returns a struct where
// a map[string]any used to be, and a struct drops any key its fields do not name
// — so this asserts the answer is still exactly {"destinations": [...]} with one
// card per registered platform, in slug order.
func TestListIsOneKeyedList(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/destinations", "acme", false, nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("list answers %d keys, want exactly 1 (destinations)", len(envelope))
	}
	var out struct {
		Destinations []DestinationStatus `json:"destinations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(out.Destinations) != len(snapshot()) {
		t.Fatalf("list carries %d cards, want one per registered platform (%d)", len(out.Destinations), len(snapshot()))
	}
	if !sort.SliceIsSorted(out.Destinations, func(i, j int) bool {
		return out.Destinations[i].Platform < out.Destinations[j].Platform
	}) {
		t.Fatal("the cards are no longer in slug order")
	}
}

// TestDisconnectIsOneKeyedAck pins the disconnect body, for the same reason.
func TestDisconnectIsOneKeyedAck(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodDelete, "/v1/destinations/ga4", "acme", true, nil)
	if code != http.StatusOK {
		t.Fatalf("disconnect want 200, got %d (%s)", code, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"disconnected":true}` {
		t.Fatalf("disconnect answers %s, want {\"disconnected\":true}", got)
	}
}

// TestTheCollectionRootHasNoTrailingSlash pins failure mode #9. The untyped
// registration declared the collection as the group's EMPTY leaf, which
// normalises to "<prefix>/", so the published document, the operationId, the MCP
// tool name and every generated SDK's URL all carried a trailing slash for a path
// this API has never served. The router is non-strict, so nothing broke on the
// wire — it was visible only in the artifact, which IS the product surface.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := destinationOps(t)
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash for a path this API has never served", key)
		}
	}
}

func sorted(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
