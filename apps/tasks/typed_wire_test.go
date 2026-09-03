package tasks

// Why this surface publishes ten operations and types none of them.
//
// The refusal itself is recorded at each mount (tasks.go). This file is the
// LEDGER and the MEASUREMENTS it rests on: one entry per served address with the
// wire fact that keeps it out of the typed registry, one test that reads the LIVE
// router and requires the two ledgers to SUM to what is served, and one test per
// blocker so a claim in the record is checkable rather than asserted.
//
// It exists because the previous record was WRONG about the address it spent the
// most words on, and prose cannot go red. It said the bare noun's redirect was
// "the engine's own ServeMux answering before anything of ours runs", and
// therefore that "there is nothing for a typed op to BE". Both halves are false
// and the tests below measure it: hanzoai/tasks v1.52.9 registers no /v1/task/
// subtree pattern, so its own handler answers that address 404 — the only such
// pattern in the request path is CLOUD's (httpMux). The answer was cloud's the
// whole time.
//
// What actually keeps it out of the registry is something nobody had measured:
// the exact route at /v1/task IS NEVER ENTERED. The greedy sibling at
// /v1/task/* matches the empty remainder and wins there in either registration
// order, so the exact registration's only remaining job is to put the address in
// the document. The reason a native handler there was tempting — cloud owns the
// answer — is true and not sufficient.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
)

// untypedByDesign is the CLOSED list of tasks addresses that are NOT typed ops,
// each with the wire fact that keeps it out. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI schema, the MCP tool, the CLI command and the
// SDK method all come from — so an operation missing from that registry is
// invisible to all four. These are missing on purpose.
//
// Keyed by PATH, not by "METHOD path": both addresses are bound with All(), so one
// wire fact refuses every method at once. Keying by method would state one fact
// five times and let four copies rot.
var untypedByDesign = map[string]string{
	"/v1/task": "answers 307 with Location /v1/task/ and a body that depends on the method — " +
		"the short fallback HTML on a GET, nothing on the rest. A typed op's only response path " +
		"is c.JSON(out) under a status it declared, which carries neither the header that is " +
		"the whole point of the call nor two content types at one address.\n\n" +
		"And a handler of ANY kind here is unreachable: the greedy /v1/task/* beside it claims " +
		"the bare noun, so this registration exists to publish the address and never to serve " +
		"it. Its answer is the wildcard's, and so are the wildcard's blockers. " +
		"TestTheBareNounAnswersEveryByte, TestTheGreedyRouteClaimsTheBareNoun, " +
		"TestTheEngineDoesNotServeTheBareNoun.",

	"/v1/task/{wildcard1}": "ONE route over the whole durable engine, whose operations are " +
		"matched by path SEGMENT inside hanzoai/tasks' own ServeMux rather than by patterns — " +
		"so there is no route here to type, their inputs are anonymous structs local to that " +
		"module's handlers, and the engine hands cloud its surface only as http.Handler. Four " +
		"blockers beyond that, each measured below: the greedy wildcard the typed registry and " +
		"this router spell differently, four content types at one address (JSON, two plain-text " +
		"refusals, an event STREAM), twelve verbs that RUN on a malformed body a typed op would " +
		"400 before the handler is entered, and an error envelope that is the engine's rather " +
		"than zip's. TestOneWildcardCarriesFourContentTypes, TestCancelIgnoresAMalformedBody, " +
		"TestEngineErrorEnvelopeIsNotZips.",
}

// withEngine points the shared-engine resolution at one this test owns, for the
// life of the test. Serve is what sets the real one (durable.go), and a package
// test does not run Serve — so without this every address here answers its
// engine-nil 503 and nothing about the surface is measurable.
func withEngine(t *testing.T) *tasks.Embedded {
	t.Helper()
	srv := testEngine(t)
	prev := engine
	engine = func() *tasks.Embedded { return srv }
	t.Cleanup(func() { engine = prev })
	return srv
}

// surfaceApp mounts the WHOLE surface through the REAL Mount, so the assertions
// below read the router the document is generated from rather than a
// reconstruction of it — a route added anywhere in that mount shows up here
// without anyone remembering to list it.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// tasksOps reads BOTH projections of the live router: what the document says is
// served, and which of those carry a typed registry entry.
func tasksOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "tasks", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a tasks operation is neither a typed op
// nor covered by a reason above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason. It fails the other way too: a reason naming an address tasks no longer
// serves is stale prose, which is exactly what this file replaced.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := tasksOps(t)
	if len(served) == 0 {
		t.Fatal("the router serves nothing — the gate would pass on an empty set")
	}

	var untyped []string
	claimed := map[string]bool{}
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		path := key[strings.Index(key, " ")+1:]
		if _, named := untypedByDesign[path]; named {
			claimed[path] = true
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command "+
			"and no SDK method. Convert it, or add its address to untypedByDesign with the wire "+
			"fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for path := range untypedByDesign {
		if !claimed[path] {
			t.Errorf("untypedByDesign names %q, which tasks no longer serves untyped", path)
		}
	}
	// The two ledgers are the whole surface, stated as a SUM so a route cannot be
	// in both and cannot be in neither. The ledger is keyed by path and the
	// surface is counted in operations, so the named side expands to the
	// operations its addresses account for.
	named := 0
	for key := range served {
		if claimed[key[strings.Index(key, " ")+1:]] {
			named++
		}
	}
	if len(typed)+named != len(served) {
		t.Errorf("typed(%d) + named(%d) != served(%d)", len(typed), named, len(served))
	}
}

// TestTheEngineDoesNotServeTheBareNoun refutes the claim this file replaced.
//
// The redirect at /v1/task is net/http's subtree rule applied to a pattern, and
// the only /v1/task/ pattern anywhere in the request path is the one CLOUD
// registers (httpMux). hanzoai/tasks' own handler — the thing that pattern fronts
// — answers the bare noun 404 on every method, so it cannot be the source of the
// answer the record attributed to it. The day the engine starts serving that
// address this goes red, and whose answer it is becomes a live question again.
func TestTheEngineDoesNotServeTheBareNoun(t *testing.T) {
	srv := testEngine(t)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(m, "/v1/task", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("engine HTTPHandler %s /v1/task = %d, want 404 — the engine now serves the "+
				"bare noun, so the redirect is no longer cloud's alone", m, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	httpMux(srv).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/task", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("cloud's own mux GET /v1/task = %d, want 307 — the pattern the redirect is "+
			"derived from has moved", rec.Code)
	}
}

// TestTheGreedyRouteClaimsTheBareNoun is the blocker itself, measured.
//
// Owning an answer is not enough to serve it: the greedy `*` matches the EMPTY
// remainder, so /v1/task/* covers /v1/task and WINS there in either
// registration order. A handler registered at the exact address is never entered,
// which makes a native one a second description of a wire it cannot produce —
// and makes the exact registration a DOCUMENT entry rather than a route.
//
// The `:param` case is the control, and it is what makes this a fact about
// greediness rather than about precedence: a named-parameter sibling does NOT
// swallow its parent, and the exact route answers.
func TestTheGreedyRouteClaimsTheBareNoun(t *testing.T) {
	who := func(build func(*zip.App)) string {
		t.Helper()
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		build(app)
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/task", nil))
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.Header.Get("X-Who")
	}
	mark := func(name string) zip.Handler {
		return func(c *zip.Ctx) error {
			c.SetHeader("X-Who", name)
			return c.NoContent(http.StatusNoContent)
		}
	}

	if got := who(func(a *zip.App) { a.All("/v1/task", mark("exact")) }); got != "exact" {
		t.Fatalf("alone, the exact route answers %q — the premise of the rest of this test is gone", got)
	}
	for _, c := range []struct {
		name  string
		build func(*zip.App)
	}{
		{"exact registered first", func(a *zip.App) {
			a.All("/v1/task", mark("exact"))
			a.All("/v1/task/*", mark("greedy"))
		}},
		{"greedy registered first", func(a *zip.App) {
			a.All("/v1/task/*", mark("greedy"))
			a.All("/v1/task", mark("exact"))
		}},
	} {
		if got := who(c.build); got != "greedy" {
			t.Errorf("%s: GET /v1/task reached %q, want greedy — the exact route is reachable "+
				"now, so /v1/task can carry a handler of its own and the refusal in "+
				"untypedByDesign is stale", c.name, got)
		}
	}
	if got := who(func(a *zip.App) {
		a.All("/v1/task", mark("exact"))
		a.All("/v1/task/:leaf", mark("param"))
	}); got != "exact" {
		t.Errorf("with a :param sibling GET /v1/task reached %q, want exact — the control for "+
			"this being about greediness rather than precedence has changed", got)
	}
}

// TestTheBareNounAnswersEveryByte pins the WHOLE answer at /v1/task on every
// method the router accepts — status, Location, Content-Type and body — driven
// through the real Mount with a live engine, which nothing here had ever done.
//
// All four fields are needed and the last two are why: an httptest recorder over
// httpMux alone cannot see the Content-Type the client is sent. net/http sets
// none on a redirect it writes no body for, so what arrives is fasthttp's own
// default — and on a HEAD net/http DOES set one, the GET's text/html, where that
// default would be text/plain. Its predecessor asserted status and Location only,
// so it measured nothing that could distinguish one implementation of this
// address from another; that is how a wrong account of it survived.
func TestTheBareNounAnswersEveryByte(t *testing.T) {
	withEngine(t)
	app := surfaceApp(t)

	const html = "text/html; charset=utf-8"
	const plain = "text/plain; charset=utf-8"
	body := `<a href="/v1/task/">Temporary Redirect</a>.` + "\n\n"

	for _, c := range []struct{ method, ct, body string }{
		{http.MethodGet, html, body},
		{http.MethodHead, html, ""}, // same content type as a GET, no body: net/http's rule
		{http.MethodPost, plain, ""},
		{http.MethodPut, plain, ""},
		{http.MethodPatch, plain, ""},
		{http.MethodDelete, plain, ""},
		{http.MethodOptions, plain, ""},
		{"TRACE", plain, ""},
	} {
		resp, err := app.Test(httptest.NewRequest(c.method, "/v1/task", nil))
		if err != nil {
			t.Fatalf("%s: %v", c.method, err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("%s /v1/task = %d, want 307", c.method, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != subtree {
			t.Errorf("%s /v1/task Location = %q, want %q", c.method, loc, subtree)
		}
		if ct := resp.Header.Get("Content-Type"); ct != c.ct {
			t.Errorf("%s /v1/task Content-Type = %q, want %q", c.method, ct, c.ct)
		}
		if string(got) != c.body {
			t.Errorf("%s /v1/task body = %q, want %q", c.method, got, c.body)
		}
	}
}

// TestTheBareNounFailsSoftWithNoEngine pins the other half of the wire: until the
// engine is live the whole surface answers 503 in the ENGINE's refusal shape
// (`code` a NUMBER), and the bare noun is not an exception to that.
func TestTheBareNounFailsSoftWithNoEngine(t *testing.T) {
	if engine() != nil {
		t.Skip("this process has an engine; the not-ready path is what is under test")
	}
	app := surfaceApp(t)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/task", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("engine-nil GET /v1/task = %d, want 503", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("engine-nil Content-Type = %q, want application/json", ct)
	}
	if string(got) != string(notReady) {
		t.Errorf("engine-nil body = %q, want %q", got, notReady)
	}
}

// TestOneWildcardCarriesFourContentTypes pins the four answer shapes that ONE
// route — app.All("/v1/task/*", …) in Mount — carries at once: the engine's
// JSON API, the text/plain 404 its ServeMux writes for a path or method it does
// not serve, the text/plain 405 the MCP endpoint writes for a non-POST, and the
// event stream.
//
// A typed op is one method at one path with one In and one Out, and it answers
// application/json or a bare status. It cannot be four content types, and it
// cannot be a stream at all: the handler returns *Out and zip encodes it once,
// after the handler is done. So the wildcard stays a raw relay, and the
// operations behind it stay outside the document.
func TestOneWildcardCarriesFourContentTypes(t *testing.T) {
	mux := httpMux(testEngine(t))
	for _, c := range []struct {
		method, path string
		code         int
		contentType  string
	}{
		{http.MethodGet, "/v1/task/namespaces", 200, "application/json"},
		{http.MethodPut, "/v1/task/namespaces", 404, "text/plain"},
		{http.MethodGet, "/v1/task/mcp", 405, "text/plain"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, validated(httptest.NewRequest(c.method, c.path, nil)))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d (%s)", c.method, c.path, rec.Code, c.code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.contentType) {
			t.Errorf("%s %s content-type = %q, want %s", c.method, c.path, ct, c.contentType)
		}
	}

	// The stream, bounded: the handler holds the connection open until the client
	// goes away, which is the property that makes it untypable.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, validated(httptest.NewRequest(http.MethodGet, "/v1/task/events", nil)).WithContext(ctx))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("GET /v1/task/events content-type = %q, want text/event-stream", ct)
	}
}

// TestEngineErrorEnvelopeIsNotZips pins the two refusal envelopes side by side,
// because their difference is a blocker for typing any single leaf out of the
// wildcard.
//
// The engine writes {"error":…,"code":403} with code a JSON NUMBER (hanzoai/tasks
// pkg/tasks/embed.go writeErr; cloud's gate writes the same shape for the
// refusal). zip writes the RFC 9457 problem document — {type, title, status,
// detail} plus `code` when the refusal names one — so the SENTENCE moves from
// `error` to `detail` and the numeric `code` a client branches on is not there at
// all. A leaf converted out of this subtree would answer in that vocabulary while
// every sibling behind the same wildcard kept the engine's, so one product surface
// would report failures two ways depending on which path a client hit.
//
// Both bodies are asserted WHOLE. An earlier version of this test declared the
// difference in prose as {"status":403,"error":…} — the pre-9457 shape — and
// checked only that `status` was a number and `code` absent, so the sentence key
// could move and it stayed green. A declared-and-unasserted field reads as
// coverage.
func TestEngineErrorEnvelopeIsNotZips(t *testing.T) {
	mux := httpMux(testEngine(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/task/namespaces", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unvalidated GET /v1/task/namespaces = %d, want 403", rec.Code)
	}
	var engineBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &engineBody); err != nil {
		t.Fatalf("engine error body is not JSON: %v", err)
	}
	if _, ok := engineBody["code"].(float64); !ok {
		t.Errorf("engine error body code = %#v, want a JSON number", engineBody["code"])
	}
	if _, ok := engineBody["error"].(string); !ok {
		t.Errorf("engine error body carries no `error` sentence: %v", engineBody)
	}

	b, err := json.Marshal(zip.ErrForbidden("identity required"))
	if err != nil {
		t.Fatalf("marshal zip error: %v", err)
	}
	const problem = `{"detail":"identity required","status":403,"title":"Forbidden","type":"about:blank"}`
	if string(b) != problem {
		t.Errorf("zip refusal = %s, want %s — re-read the refusals in untypedByDesign against "+
			"the pinned zip before trusting them", b, problem)
	}
}

// TestCancelIgnoresAMalformedBody pins the twelfth-of-its-kind handler: the
// workflow verbs (cancel/terminate/signal and their batch siblings) DISCARD a
// body decode error — `_ = decode(r, &req)` in hanzoai/tasks pkg/tasks/embed.go
// — because the body is optional detail (reason, identity) on a verb the URL
// already fully addresses. Its strict sibling, the workflow START on the same
// subtree, refuses the same bytes with 400.
//
// A typed op cannot be tolerant: op.invoke returns ErrBadRequest on any body it
// cannot decode, before the handler runs (zip typed.go, op.invoke). So even a
// decomposition of the wildcard could not type these twelve without turning a
// tolerated request into a 400.
func TestCancelIgnoresAMalformedBody(t *testing.T) {
	mux := httpMux(testEngine(t))

	post := func(path, body string) (int, string) {
		r := validated(httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body))))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}

	if code, body := post("/v1/task/namespaces",
		`{"namespaceInfo":{"name":"smoke","state":"NAMESPACE_STATE_REGISTERED"},`+
			`"config":{"workflowExecutionRetentionTtl":"24h"}}`); code != http.StatusOK {
		t.Fatalf("register namespace = %d: %s", code, body)
	}

	// Malformed body, tolerated: the verb runs and fails on the workflow it was
	// asked about, never on the bytes.
	code, body := post("/v1/task/namespaces/smoke/workflows/nope/cancel", `{`)
	if code == http.StatusBadRequest {
		t.Errorf("cancel now refuses a malformed body (%d %s) — re-check the refusal", code, body)
	}
	if !strings.Contains(body, "not found") {
		t.Errorf("cancel with a malformed body = %d %s, want the workflow lookup to have run", code, body)
	}

	// The strict sibling, same bytes, same subtree.
	if code, body := post("/v1/task/namespaces/smoke/workflows", `{`); code != http.StatusBadRequest {
		t.Errorf("workflow start with a malformed body = %d %s, want 400", code, body)
	}
}

// validated presents r as a caller cloud's identity boundary has already
// resolved: X-User-Id is minted only from a verified credential, and gate admits
// it only alongside an org (apps/principal.OrgOf decides both).
func validated(r *http.Request) *http.Request {
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-User-Id", "u-acme")
	return r
}
