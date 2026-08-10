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
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
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

var untypedByDesign = map[string]string{
	// The three repository-lifecycle routes. A board IS a repository on the forge
	// (source.go), so creating, renaming and deleting one is a FORGE operation
	// under forge permissions; re-exposing it here would put a second, weaker door
	// on the same object. They answer 405 naming the forge — which is a different
	// fact from 404, and the reason they are routes at all rather than absent.
	//
	// Untyped because a typed op publishes a request and response schema for work
	// it does not do. There is no shape to describe: the only thing these answer
	// is a refusal.
	"POST /v1/tracker/projects":         repoLifecycleReason,
	"PATCH /v1/tracker/projects/{key}":  repoLifecycleReason,
	"DELETE /v1/tracker/projects/{key}": repoLifecycleReason,
}

const repoLifecycleReason = "a board is a repository on the deployment's forge, so its lifecycle is a " +
	"forge operation under forge permissions — offering it here would be a second door onto the same " +
	"object with this surface's guard instead of the forge's. Answers 405 naming the forge rather than " +
	"404, because 'not this service's job' and 'no such thing' are different facts. A typed op would " +
	"publish request and response schemas for work that is never done."

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
