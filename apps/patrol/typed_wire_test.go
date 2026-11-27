package patrol

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// typed_wire_test.go measures this package's published surface rather than
// asserting it in prose: a route added untyped goes red without anyone
// remembering to name it, a reason naming a route patrol no longer serves goes
// red too, and the two ledgers must sum to what the live router serves.

// untypedByDesign is the closed list of operations here that are not typed ops,
// each with the wire fact that keeps it raw. Addresses are written the way the
// document writes them.
var untypedByDesign = map[string]string{
	"GET /v1/patrol/feed": "an open Server-Sent Events response written by a loop that outlives the handler " +
		"(c.SendStreamWriter); a typed op returns one marshalled value and there is no In/Out for a feed.",
	"GET /v1/patrol/report/{name}": "a PDF is bytes under a filename, and the same route answers text/plain " +
		"for ?format=text; no In/Out pair describes either.",
}

// mountAlone mounts only this subsystem, so the projection below reads this
// package's surface and not the framework engine's.
func mountAlone(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("mount patrol: %v", err)
	}
	return app
}

// surface reads both projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry.
func surface(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountAlone(t)
	doc, err := spec.Of(app, spec.Info{Title: Module, Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
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

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed
// op nor one named above, so the next route added is typed by default and
// dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := surface(t)

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
			t.Errorf("untypedByDesign names %q, which patrol no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestRouteTableIsServed pins the addresses the front end and the receiving
// centre call, so a rename shows up here rather than as a 404 in a control room.
func TestRouteTableIsServed(t *testing.T) {
	served, _ := surface(t)
	for _, want := range []string{
		"GET /v1/patrol/tenant/{org}",
		"GET /v1/patrol/snapshot",
		"GET /v1/patrol/site", "GET /v1/patrol/site/{name}", "PUT /v1/patrol/site/{name}",
		"GET /v1/patrol/unit", "GET /v1/patrol/unit/near",
		"POST /v1/patrol/unit/{name}/fix", "POST /v1/patrol/unit/{name}/state",
		"GET /v1/patrol/tour", "POST /v1/patrol/point/{name}/confirm",
		"GET /v1/patrol/key", "GET /v1/patrol/key/{name}", "POST /v1/patrol/key/{name}/move",
		"GET /v1/patrol/camera", "GET /v1/patrol/camera/{name}/stream",
		"GET /v1/patrol/event", "POST /v1/patrol/event",
		"GET /v1/patrol/incident", "GET /v1/patrol/incident/{name}",
		"POST /v1/patrol/incident", "POST /v1/patrol/incident/{name}/step",
		"GET /v1/patrol/note", "POST /v1/patrol/note",
		"POST /v1/patrol/alarm", "GET /v1/patrol/feed",
		"POST /v1/patrol/report", "GET /v1/patrol/report/{name}",
	} {
		if !served[want] {
			t.Errorf("%s is not served", want)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose is the product surface: it becomes the OpenAPI description
// and the MCP tool description a model reads to pick the tool. zipdoc_gen.go
// carries it into the binary, so an op added without regenerating shows up here
// as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := surface(t)
	if len(typed) == 0 {
		t.Fatal("no typed patrol ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: make -C apps/patrol generate", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the response side the op-level gate
// cannot see. A typed op publishes its Out's whole schema, and a property that
// reaches openapi.yaml with no description reaches every generated SDK and every
// MCP inputSchema without one too.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountAlone(t)
	doc, err := spec.Of(app, spec.Info{Title: Module, Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
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
			"Write the field's doc comment and run: make -C apps/patrol generate",
			len(bare), strings.Join(bare, ", "))
	}
}
