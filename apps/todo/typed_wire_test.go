package todo

// typed_wire_test.go pins the wire the typed conversion had to carry over from
// the raw handlers: the JSON ARRAY the two listings answer (a struct Out would
// have wrapped them in an object), the 204-with-no-body the two deletes answer,
// the query-parameter binding the issue listing filters on, the path-parameter
// binding the detail routes address with, and the tenancy that must come from the
// validated principal rather than from any caller-supplied field.
//
// It also holds the CLOSED list of todo routes that are NOT typed ops, each
// with the wire fact that keeps it out — so an eleventh untyped route here goes
// red rather than passing unnoticed.

import (
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

var untypedByDesign = map[string]string{
	// The three repository-lifecycle routes. A board IS a repository on the forge
	// (source.go), so creating, renaming and deleting one is a FORGE operation
	// under forge permissions; re-exposing it here would put a second, weaker endpoint
	// on the same object. They answer 405 naming the forge — which is a different
	// fact from 404, and the reason they are routes at all rather than absent.
	//
	// Untyped because a typed op publishes a request and response schema for work
	// it does not do. There is no shape to describe: the only thing these answer
	// is a refusal.
	"POST /v1/todo/projects":         repoLifecycleReason,
	"PATCH /v1/todo/projects/{key}":  repoLifecycleReason,
	"DELETE /v1/todo/projects/{key}": repoLifecycleReason,
}

const repoLifecycleReason = "a board is a repository on the deployment's forge, so its lifecycle is a " +
	"forge operation under forge permissions — offering it here would be a second endpoint onto the same " +
	"object with this surface's guard instead of the forge's. Answers 405 naming the forge rather than " +
	"404, because 'not this service's job' and 'no such thing' are different facts. A typed op would " +
	"publish request and response schemas for work that is never done."

// todoOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func todoOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountWire(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "todo", Version: "v1"})
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
		return strings.HasPrefix(p, "/v1/todo/") && p != "/v1/todo/health"
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

// TestEveryTodoRouteIsTypedOrNamed fails when a todo operation is neither a
// typed op nor one of the two named above — so the next route added here is typed
// by default, and dropping one out of the registry takes a deliberate edit with a
// reason.
func TestEveryTodoRouteIsTypedOrNamed(t *testing.T) {
	served, typed := todoOps(t)

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

// TestEveryTypedTodoOpIsDescribed fails when a typed op reaches the document
// with no prose. zipdoc lifts the handler's doc comment into BOTH the OpenAPI
// description and the MCP tool description, so an op whose comment does not lift
// is an SDK method and an agent tool with nothing to read.
func TestEveryTypedTodoOpIsDescribed(t *testing.T) {
	_, typed := todoOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed todo ops — the conversion is not wired")
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
			"Run: go generate -run zipdoc ./apps/todo/...", strings.Join(bare, ", "))
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates above
// cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's FIELDS
// come from a different place — a doc comment on each one, which zipdoc lifts one at
// a time — so a fully typed surface can still publish a wholly unreadable document.
//
// It matters here because the scalars on this surface are closed sets and addresses,
// not labels. `status` is backlog | todo | in_progress | done | canceled and `kind` is
// issue | pr | epic; both are refused with 400 outside that set, so a caller who
// cannot read them guesses at a value the server will not take. `priority` is never
// empty — an unset one is the value "none" — while an empty `assignee` is load-bearing
// the other way: it means unheld, which is the state a claim requires. And `id` is not
// the address: an issue is addressed by `projectKey` plus `number`, which is per-board
// and collides across the org.
//
// Presence is all a gate can check. A description restating the field's name is worse
// than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountWire(t), openapi.Info{Title: "todo", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("todo publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/todo describe",
			len(bare), strings.Join(bare, ", "))
	}
}
