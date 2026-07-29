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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// typedOps is every route registered with zip.<Verb>(g, …) in Mount. A route
// leaving this list is a projection regression; one joining it is the migration
// working, and the list is the place to say so.
var typedOps = []string{
	"DELETE /v1/framework/:doctype/:name",
	"DELETE /v1/framework/doctypes/:name",
	"DELETE /v1/framework/roles/:user/:role",
	"GET /v1/framework/:doctype",
	"GET /v1/framework/:doctype/:name",
	"GET /v1/framework/doctypes",
	"GET /v1/framework/doctypes/:name",
	"GET /v1/framework/modules",
	"GET /v1/framework/modules/:module",
	"GET /v1/framework/roles",
	"GET /v1/framework/summary",
	"POST /v1/framework/:doctype/:name/cancel",
	"POST /v1/framework/:doctype/:name/submit",
	"POST /v1/framework/doctypes",
	"POST /v1/framework/modules/:module/install",
	"POST /v1/framework/roles",
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
// else: an SDK method that cannot send a document. They convert when zip can
// declare an open-object input (additionalProperties: true).
var rawRoutes = map[string]string{
	"POST /v1/framework/:doctype":      "free-form document body: shape is metadata, not a Go type",
	"PUT /v1/framework/:doctype/:name": "free-form document body: shape is metadata, not a Go type",
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
		"createDocType defines a DocType in the caller's org", // an op's prose
		"Data is every DocType defined in the caller's org",   // an Out field's prose
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
	for _, path := range []string{"/v1/framework/doctypes", "/v1/framework/roles", "/v1/framework/modules"} {
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
