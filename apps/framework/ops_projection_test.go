package framework

// The point of a typed op is that ONE registration feeds every projection. This
// pins the three properties that would silently break it: the surface is exactly
// the ops plus the routes that deliberately stay raw, the typed ones actually
// reached the registry the OpenAPI document / MCP tool list / CLI are read from,
// and they refuse an invocation that arrives with no principal at all.
//
// The wire pins at the bottom cover what the round-trip tests do not: the empty
// collection, which must serialise as [] and not null, and the summary body.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// typedOps is every route registered with zip.<Verb>(g, …) in Mount. A route
// leaving this list is a projection regression; one joining it is the migration
// working, and the list is the place to say so.
var typedOps = []string{
	"DELETE /v1/framework/:doctype/:name",
	"DELETE /v1/framework/doctypes/:name",
	"GET /v1/framework/:doctype",
	"GET /v1/framework/:doctype/:name",
	"GET /v1/framework/doctypes",
	"GET /v1/framework/doctypes/:name",
	"GET /v1/framework/modules",
	"GET /v1/framework/modules/:module",
	"GET /v1/framework/summary",
	"POST /v1/framework/:doctype/:name/cancel",
	"POST /v1/framework/:doctype/:name/submit",
	"POST /v1/framework/doctypes",
	"POST /v1/framework/modules/:module/install",
	"PUT /v1/framework/doctypes/:name",
}

// rawRoutes is every route that stays a raw handler, with the reason. Registered
// here so the reason is checked rather than merely written down.
//
// ONE reason remains, and it is a property of the SCHEMA, not of effort: a typed
// op's request schema is REFLECTED off its In type, and the body of a document
// write is the document's own field data — an open object the DocType defines at
// run time. No Go struct both accepts it verbatim and describes it, so typing
// these two would publish a request schema naming the path segments and nothing
// else: an SDK method that cannot send a document.
//
// It takes THREE halves of one capability, and a partial fix converts nothing.
// zip must be able to DECLARE an open object — today `map[string]any` projects
// `additionalProperties: {"type":"object"}`, which asserts every field VALUE is a
// JSON object and is refuted by every document these tests send. bindURL must
// be able to BIND the URL onto one: it returns early unless the In is a struct, so
// an open-object In carries no :doctype/:name while a struct In carries no
// document. And the bound params must ride OUTSIDE the body namespace: off the
// REST path op.invoke receives no path map (MCP tools/call and the call plane
// pass nil), so URL params could only travel as body keys — and for a
// prompt-named DocType the create body's `name` IS the document's name (engine
// ops.go `stringField(in, "name")` → doctype ResolveName), so folding :name into
// the body collides with a key the document owns. Re-verified against zip v1.18.6
// (the pin) and v1.18.8 (the newest published tag): none of the three shipped.
// TestOpenObjectRefusalStillHolds now reads ALL THREE, so no leg of this can
// outlive its cause.
//
// What it COSTS, measured rather than assumed: these two reach openapi.yaml as
// route-only entries — path parameters, no requestBody, no responses, no prose
// (plugin/framework/openapi.json). An SDK method generated from that cannot send
// a document either, so the choice here is not "typed and broken vs. absent", it
// is "broken with a schema that lies about the body vs. broken with no schema at
// all". The second is the honest one, and it is the one that goes away when the
// capability lands rather than having to be un-published.
//
// Leg 1 is not only a blocker — it is a LIVE defect in the published document.
// docView is already the Out of four typed ops (get/submit/cancel one document,
// and the items of the list), so openapi.yaml today tells every SDK and every
// agent that each field of a returned document is a JSON object. The fix is one
// `case reflect.Interface` in zip's schemaOf returning `{}` (JSON Schema "any");
// it needs a zip release, so it is not made here.
var rawRoutes = map[string]string{
	"POST /v1/framework/:doctype":      "free-form document body: shape is metadata, not a Go type",
	"PUT /v1/framework/:doctype/:name": "free-form document body: shape is metadata, not a Go type",
}

// TestOpenObjectRefusalStillHolds makes the refusal above EXPIRE on its own. The
// reason cites three properties, and until this test nothing read any of them —
// so the day zip gains the capability nothing here goes red and the refusal
// outlives its cause, which is how a considered decision becomes stale prose.
//
// It pins shapes and facts that are INCONVENIENT on purpose. Each assertion
// failing is the GOOD news: a leg of the refusal has expired, and once all three
// have, POST /v1/framework/:doctype and PUT /v1/framework/:doctype/:name can
// finally become typed ops. Do not "correct" the expectations to keep it green —
// convert the two routes and delete it.
func TestOpenObjectRefusalStillHolds(t *testing.T) {
	app := mountApp(t)

	// 1. zip cannot DECLARE an open object. A document IS map[string]any, and
	// schemaOf has no reflect.Interface case, so the element type falls through
	// to the default and every field VALUE is published as a JSON object. This is
	// SHIPPED and FALSE: TestDocTypeAndDocumentRoundTrip reads a document back
	// with a string and a number in it, neither of which this schema admits.
	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema map[string]any `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	got := doc.Paths["/v1/framework/{doctype}/{name}"]["get"].Responses["200"].Content["application/json"].Schema
	want := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET one document publishes response schema %v, pinned as %v — if the value type is no longer\n"+
			"constrained, zip can declare an open object: convert the two document writes and delete this test", got, want)
	}

	// 2. zip cannot BIND the URL onto one. bindURL returns early unless the In is
	// a struct, so an open-object In never receives :doctype — checked on docView,
	// the very type a typed document write would have to bind.
	probe := zip.New(zip.Config{Logger: luxlog.New("test")})
	var bound docView
	zip.Post(probe, "/probe/:doctype", func(_ context.Context, in *docView) (*docView, error) {
		bound = *in
		return in, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/probe/Task", bytes.NewReader([]byte(`{"subject":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	if _, err := probe.Test(req); err != nil {
		t.Fatalf("probe request: %v", err)
	}
	if bound["subject"] != "x" {
		t.Fatalf("probe never reached the handler (%v) — the probe is broken, not zip", bound)
	}
	if v, ok := bound["doctype"]; ok {
		t.Errorf("zip bound :doctype=%v onto an open-object In — it can now carry the URL:\n"+
			"convert the two document writes (and check the params ride outside the body namespace)", v)
	}

	// 3. The bound params could only ride INSIDE the body namespace, and `name`
	// there is already taken. Off the REST path op.invoke receives no path map at
	// all (zip mcp.go, `op.invoke(…, params.Arguments, nil, nil)`), so a typed
	// write's :doctype/:name could only travel as body keys — and for a
	// prompt-named DocType the create body's `name` IS the document's name, read
	// straight off it (engine ops.go `stringField(in, "name")` → doctype
	// ResolveName). Folding :name into the body would therefore collide with a key
	// the document owns, which is a wire change, not a description.
	//
	// Asserted over the live wire rather than left as prose: the day the engine
	// stops naming a document from its body, THIS leg of the refusal has expired
	// too, and it should go red saying so.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", map[string]any{
		"name": "Ticket", "autoname": "prompt",
		"fields": []map[string]any{{"fieldname": "subject", "fieldtype": "Data"}},
	})
	code, created := do(t, app, http.MethodPost, "/v1/framework/Ticket", "acme",
		map[string]any{"name": "chosen-name", "subject": "x"})
	var made struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(created, &made)
	if code != http.StatusCreated || made.Name != "chosen-name" {
		t.Errorf("create with a body `name` = %d %s, pinned as 201 named %q — if the body no longer names\n"+
			"the document, :name can ride in the body namespace: re-check leg 3 of the refusal",
			code, created, "chosen-name")
	}
}

// TestSurfaceIsRegistered checks that every op above is a live route and that the
// typed ones reached the registry, so the REST surface and the projected surfaces
// cannot drift apart.
func TestSurfaceIsRegistered(t *testing.T) {
	app := mountApp(t)

	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" { // fiber mirrors every GET; not a surface of ours
			continue
		}
		if !strings.HasPrefix(r.Path, "/v1/framework") {
			continue // zip's own built-ins, not this subsystem's
		}
		live[r.Method+" "+r.Path] = true
	}
	// The surface is EXACTLY the typed ops plus the raw ones. A route that is
	// neither is a route nobody decided on.
	if len(live) != len(typedOps)+len(rawRoutes) {
		t.Errorf("live /v1/framework routes = %d, want %d (%d typed + %d raw)",
			len(live), len(typedOps)+len(rawRoutes), len(typedOps), len(rawRoutes))
	}
	for _, op := range typedOps {
		if !live[op] {
			t.Errorf("typed op %s is not a live route", op)
		}
	}
	for route, why := range rawRoutes {
		if !live[route] {
			t.Errorf("raw route %s (%s) is not a live route", route, why)
		}
	}

	// The registry, read through the CLI projection — the same app.ops the
	// OpenAPI document and the MCP tool list are built from.
	got := make([]string, 0, len(typedOps))
	for _, c := range app.Commands() {
		got = append(got, c.Method+" "+c.Path)
	}
	sort.Strings(got)
	want := append([]string(nil), typedOps...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("registry ops:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestProjectionsFailClosed is the security half of making these ops projections.
// zip publishes every typed op as an MCP tool and a CLI command, and NEITHER
// passes through the route group, so neither carries the bridges that park the
// validated org and the caller's identity. Every op must therefore refuse an
// invocation that arrives that way — the engine's own "valid principal required",
// which is the same 403 an unvalidated REST call gets, with no second gate to
// keep in sync.
func TestProjectionsFailClosed(t *testing.T) {
	app := mountApp(t)
	for _, cmd := range app.Commands() {
		// A bare context: what LocalInvoke hands an op off the HTTP path.
		_, err := zip.LocalInvoke(context.Background(), cmd, nil, []byte(`{}`))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Errorf("%s %s off the HTTP path: err=%v, want 403", cmd.Method, cmd.Path, err)
		}
	}
}

// TestSpecCarriesProse proves the doc comments are LIVE, not merely written. Go
// drops comments at compile time, so the only path from source to spec is the
// build-time cmd/zipdoc pass (//go:generate in framework.go) that emits
// zipdoc_gen.go. A handler comment edited without re-running it, or the generated
// file deleted, leaves the spec describing nothing — this fails when that happens.
func TestSpecCarriesProse(t *testing.T) {
	spec, err := json.Marshal(mountApp(t).OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	for _, want := range []string{
		// Substrings that do not cross a source line break — the lift keeps the
		// comment's own wrapping, so a wrapped phrase would never match.
		// An op's prose, WITHOUT the leading `createDocType` its source comment
		// opens with: the identifier belongs to Go's namespace, and zip strips an
		// exact leading match of the handler's own name on the way out (v1.18.13).
		"Defines a DocType in the caller's org",
		"Data is every DocType defined in the caller's org", // an Out field's prose
		"TASK-00001", // an Example:
	} {
		if !strings.Contains(string(spec), want) {
			t.Errorf("openapi spec is missing %q — re-run `go generate -run zipdoc ./apps/framework/...`", want)
		}
	}
}

// TestEmptyCollectionsAreArrays pins the shape the raw handlers sent: an org with
// nothing defined answers `{"data":[]}`, never `{"data":null}`. A client that
// ranges over the value breaks on null, and the difference is invisible in a
// length assertion — which is why it is asserted on the BYTES.
func TestEmptyCollectionsAreArrays(t *testing.T) {
	app := mountApp(t)
	for _, path := range []string{"/v1/framework/doctypes", "/v1/framework/modules"} {
		code, body := do(t, app, http.MethodGet, path, "fresh", nil)
		if code != http.StatusOK || string(body) != `{"data":[]}` {
			t.Errorf("GET %s on an empty org = %d %s, want 200 {\"data\":[]}", path, code, body)
		}
	}
	// The document list too, once its DocType exists but has no rows.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "fresh", taskDocType())
	code, body := do(t, app, http.MethodGet, "/v1/framework/Task", "fresh", nil)
	if code != http.StatusOK || string(body) != `{"data":[]}` {
		t.Errorf("GET empty document list = %d %s, want 200 {\"data\":[]}", code, body)
	}
}

// TestSummaryWire pins the summary body. It is a restated shape (summaryView, not
// engine.Summary, because "Summary" is already another app's schema name), so the
// wire it sends has to be asserted rather than assumed.
func TestSummaryWire(t *testing.T) {
	app := mountApp(t)
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", taskDocType())
	do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"subject": "x"})
	code, body := do(t, app, http.MethodGet, "/v1/framework/summary", "acme", nil)
	if code != http.StatusOK || string(body) != `{"doctypes":1,"documents":1}` {
		t.Errorf("GET /v1/framework/summary = %d %s, want 200 {\"doctypes\":1,\"documents\":1}", code, body)
	}
}
