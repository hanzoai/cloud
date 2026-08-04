package tracker

// typed_wire_test.go pins the wire the typed conversion had to carry over from
// the raw handlers: the JSON ARRAY the two listings answer (a struct Out would
// have wrapped them in an object), the 204-with-no-body the two deletes answer,
// the query-parameter binding the issue listing filters on, the path-parameter
// binding the detail routes address with, and the tenancy that must come from the
// validated principal rather than from any caller-supplied field.
//
// It also holds the CLOSED list of tracker routes that are NOT typed ops, each
// with the wire fact that keeps it out — so an eleventh untyped route here goes
// red rather than passing unnoticed.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
)

const wireTimeout = 10 * time.Second

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// doWire issues one request as a VALIDATED principal of org and returns the
// status and the raw body — raw, because half the assertions here are about
// whether the body is a JSON array, an object, or nothing at all.
func doWire(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org) // a validated principal (principal.Org gate)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestTypedOpsPreserveTheTrackerWire(t *testing.T) {
	app := mountWire(t)
	const org = "org_wire"

	code, raw := doWire(t, app, http.MethodPost, "/v1/tracker/projects", org,
		map[string]any{"key": "ENG", "name": "Engineering"})
	if code != http.StatusCreated {
		t.Fatalf("create project: %d %s", code, raw)
	}
	code, raw = doWire(t, app, http.MethodPost, "/v1/tracker/projects/ENG/issues", org,
		map[string]any{"title": "first", "kind": "pr", "repo": "hanzoai/cloud"})
	if code != http.StatusCreated {
		t.Fatalf("create issue: %d %s", code, raw)
	}

	t.Run("the listings answer a JSON ARRAY, not an object", func(t *testing.T) {
		for _, path := range []string{"/v1/tracker/projects", "/v1/tracker/projects/ENG/issues"} {
			code, raw := doWire(t, app, http.MethodGet, path, org, nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s: %d %s", path, code, raw)
			}
			var arr []map[string]any
			if err := json.Unmarshal(raw, &arr); err != nil {
				t.Fatalf("GET %s did not answer an array: %v (%s)", path, err, raw)
			}
			if len(arr) != 1 {
				t.Errorf("GET %s returned %d rows, want 1", path, len(arr))
			}
		}
	})

	t.Run("an empty listing is [] and never null", func(t *testing.T) {
		code, raw := doWire(t, app, http.MethodGet, "/v1/tracker/projects", "org_empty", nil)
		if code != http.StatusOK {
			t.Fatalf("GET as a fresh org: %d %s", code, raw)
		}
		if strings.TrimSpace(string(raw)) != "[]" {
			t.Errorf("empty listing = %s, want []", raw)
		}
	})

	t.Run("the issue filters bind from the QUERY string", func(t *testing.T) {
		code, raw := doWire(t, app, http.MethodGet, "/v1/tracker/projects/ENG/issues?kind=pr&repo=hanzoai/cloud", org, nil)
		if code != http.StatusOK {
			t.Fatalf("filtered list: %d %s", code, raw)
		}
		var arr []map[string]any
		_ = json.Unmarshal(raw, &arr)
		if len(arr) != 1 {
			t.Errorf("kind=pr&repo=… returned %d rows, want 1", len(arr))
		}
		// A filter outside its closed set is refused, never silently empty.
		if code, _ := doWire(t, app, http.MethodGet, "/v1/tracker/projects/ENG/issues?kind=nope", org, nil); code != http.StatusBadRequest {
			t.Errorf("unknown kind filter = %d, want 400", code)
		}
		// And the project 404 still WINS over a bad filter — the raw handler
		// resolved the project before it validated the query, and so must the op.
		if code, _ := doWire(t, app, http.MethodGet, "/v1/tracker/projects/NOPE/issues?kind=nope", org, nil); code != http.StatusNotFound {
			t.Errorf("bad filter on a missing project = %d, want 404", code)
		}
	})

	t.Run("a non-numeric issue number is 400, and a missing project still wins", func(t *testing.T) {
		if code, _ := doWire(t, app, http.MethodGet, "/v1/tracker/projects/ENG/issues/abc", org, nil); code != http.StatusBadRequest {
			t.Errorf("GET issues/abc = %d, want 400", code)
		}
		if code, _ := doWire(t, app, http.MethodGet, "/v1/tracker/projects/NOPE/issues/abc", org, nil); code != http.StatusNotFound {
			t.Errorf("GET a bad number under a missing project = %d, want 404", code)
		}
	})

	t.Run("the detail routes address by path, case-insensitively", func(t *testing.T) {
		code, raw := doWire(t, app, http.MethodGet, "/v1/tracker/projects/eng", org, nil)
		if code != http.StatusOK {
			t.Fatalf("GET projects/eng: %d %s", code, raw)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["key"] != "ENG" {
			t.Errorf("key = %v, want ENG", p["key"])
		}
	})

	t.Run("a PATCH omitting a field leaves it alone", func(t *testing.T) {
		code, raw := doWire(t, app, http.MethodPatch, "/v1/tracker/projects/ENG", org,
			map[string]any{"description": "the board"})
		if code != http.StatusOK {
			t.Fatalf("patch: %d %s", code, raw)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["name"] != "Engineering" {
			t.Errorf("name = %v after a description-only patch, want it untouched", p["name"])
		}
		if p["description"] != "the board" {
			t.Errorf("description = %v, want %q", p["description"], "the board")
		}
	})

	t.Run("a PATCH body cannot smuggle a different target", func(t *testing.T) {
		// The URL is the addressing authority: zip binds the path LAST, so a body
		// field named for a path parameter never wins. Without that, a caller could
		// name one project in the URL and another in the body.
		code, raw := doWire(t, app, http.MethodPatch, "/v1/tracker/projects/ENG", org,
			map[string]any{"key": "OTHER", "name": "Renamed"})
		if code != http.StatusOK {
			t.Fatalf("patch: %d %s", code, raw)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["key"] != "ENG" {
			t.Errorf("key = %v — the body overrode the URL", p["key"])
		}
	})

	t.Run("a PATCH still REQUIRES a JSON body", func(t *testing.T) {
		// zip's typed decode skips an empty body and leaves the In at its zero
		// value, so without requireBody every bodyless PATCH would have turned
		// from 400 into 200-with-nothing-changed. Measured against the untyped
		// handler: all three of these answered 400 before the conversion.
		for _, tc := range []struct{ name, ctype, body string }{
			{"no body, no content-type", "", ""},
			{"no body, json content-type", "application/json", ""},
			{"json body, text content-type", "text/plain", `{"name":"X"}`},
		} {
			rq := httptest.NewRequest(http.MethodPatch, "/v1/tracker/projects/ENG", strings.NewReader(tc.body))
			if tc.ctype != "" {
				rq.Header.Set("Content-Type", tc.ctype)
			}
			rq.Header.Set("X-Org-Id", org)
			rq.Header.Set("X-User-Id", "u_"+org)
			resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("PATCH with %s = %d, want 400", tc.name, resp.StatusCode)
			}
		}
	})

	t.Run("the ONE declared residual: an unparseable body now outranks the 404", func(t *testing.T) {
		// zip refuses a body it cannot parse BEFORE the handler runs, so a request
		// that is both unparseable AND aimed at a missing project answers 400 where
		// the raw handler answered 404 (it bound the body after the lookup). It is
		// the only measured wire difference in this conversion; it is here so it is
		// a recorded fact rather than a surprise, and so a future zip that can defer
		// the decode turns this red instead of passing silently.
		rq := httptest.NewRequest(http.MethodPatch, "/v1/tracker/projects/NOPE", strings.NewReader(`{bad`))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
		resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("unparseable body on a missing project = %d, want the declared 400", resp.StatusCode)
		}
	})

	t.Run("the deletes answer 204 with NO body", func(t *testing.T) {
		code, raw := doWire(t, app, http.MethodDelete, "/v1/tracker/projects/ENG/issues/1", org, nil)
		if code != http.StatusNoContent {
			t.Fatalf("delete issue = %d, want 204", code)
		}
		if len(raw) != 0 {
			t.Errorf("delete issue body = %q, want empty", raw)
		}
		code, raw = doWire(t, app, http.MethodDelete, "/v1/tracker/projects/ENG", org, nil)
		if code != http.StatusNoContent {
			t.Fatalf("delete project = %d, want 204", code)
		}
		if len(raw) != 0 {
			t.Errorf("delete project body = %q, want empty", raw)
		}
		if code, _ := doWire(t, app, http.MethodDelete, "/v1/tracker/projects/ENG", org, nil); code != http.StatusNotFound {
			t.Errorf("second delete = %d, want 404", code)
		}
	})
}

// TestTenancyIsNeverACallerField pins the one rule a typed op can silently break:
// the org must come from the VALIDATED principal (cloud.Bridge parks it), never
// from an In field. An unvalidated caller — no X-User-Id, so principal.Org
// refuses — must be refused on every op, and a caller of one org must never read
// another's board even by naming it.
func TestTenancyIsNeverACallerField(t *testing.T) {
	app := mountWire(t)

	if code, _ := doWire(t, app, http.MethodPost, "/v1/tracker/projects", "acme",
		map[string]any{"key": "SEC", "name": "Secret"}); code != http.StatusCreated {
		t.Fatalf("seed acme project")
	}

	// A different org cannot see acme's board, even addressing it by key.
	if code, _ := doWire(t, app, http.MethodGet, "/v1/tracker/projects/SEC", "other", nil); code != http.StatusNotFound {
		t.Errorf("cross-org GET = %d, want 404", code)
	}
	if code, _ := doWire(t, app, http.MethodDelete, "/v1/tracker/projects/SEC", "other", nil); code != http.StatusNotFound {
		t.Errorf("cross-org DELETE = %d, want 404", code)
	}

	// No validated principal: every op refuses, none leaks a row.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/tracker/projects"},
		{http.MethodGet, "/v1/tracker/projects/SEC"},
		{http.MethodPatch, "/v1/tracker/projects/SEC"},
		{http.MethodDelete, "/v1/tracker/projects/SEC"},
		{http.MethodGet, "/v1/tracker/projects/SEC/issues"},
		{http.MethodGet, "/v1/tracker/projects/SEC/issues/1"},
		{http.MethodPatch, "/v1/tracker/projects/SEC/issues/1"},
		{http.MethodDelete, "/v1/tracker/projects/SEC/issues/1"},
	} {
		if code, _ := doWire(t, app, tc.method, tc.path, "", nil); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// untypedByDesign is the CLOSED list of tracker operations that are NOT typed
// ops, each with the wire fact that keeps it out. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These two are missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/tracker/projects": "runs the pre-create balance gate and renders its denial with " +
		"cloud.DenyResource — the fleet's NESTED {\"error\":{\"code\",\"message\"}} at 402/503. A typed " +
		"op can only refuse by RETURNING an error, which zip renders as its flat {status,code,error}; " +
		"writing the nested body from inside the op does not escape it either, because a nil Out makes " +
		"zip stamp cmp.Or(op.Status, 204) over the 402 it just wrote. The fee is 0 by default, but " +
		"CLOUD_TRACKER_FEE_CENTS_PROJECT prices it, and a route that changes shape under a supported " +
		"configuration has changed shape.",
	"POST /v1/tracker/projects/{key}/issues": "same pre-create balance gate, same nested denial, " +
		"priced by CLOUD_TRACKER_FEE_CENTS_ISSUE.",
}

// trackerOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func trackerOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountWire(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "tracker", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	// The health route is Serve's, not this package's — it is registered by the
	// host, never by routes(), so it is not this gate's business.
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/tracker/") && p != "/v1/tracker/health"
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

// TestEveryTrackerRouteIsTypedOrNamed fails when a tracker operation is neither a
// typed op nor one of the two named above — so the next route added here is typed
// by default, and dropping one out of the registry takes a deliberate edit with a
// reason.
func TestEveryTrackerRouteIsTypedOrNamed(t *testing.T) {
	served, typed := trackerOps(t)

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
			"the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this router does not serve", key)
		}
	}
}

// TestEveryTypedTrackerOpIsDescribed fails when a typed op reaches the document
// with no prose. zipdoc lifts the handler's doc comment into BOTH the OpenAPI
// description and the MCP tool description, so an op whose comment does not lift
// is an SDK method and an agent tool with nothing to read.
func TestEveryTypedTrackerOpIsDescribed(t *testing.T) {
	_, typed := trackerOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed tracker ops — the conversion is not wired")
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
			"Run: go generate -run zipdoc ./apps/tracker/...", strings.Join(bare, ", "))
	}
}
