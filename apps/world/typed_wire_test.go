package world

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountWorldOnly mounts ONLY this subsystem and returns the app, for the projection
// gate below: mountWorld also hands back the package-level service, which the gate
// does not need.
func mountWorldOnly(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// typed_wire_test.go MEASURES the two facts typing /v1/world could have moved and
// did not: the (org, project) scope stays SERVER-resolved, and a body naming a
// project is still ignored by the pipeline write.

// TestProjectIsNeverAnInputField is the reason scopeOf reaches the request instead
// of declaring `project` on an In.
//
// zip binds an In field from the BODY as well as the URL, so a `Project` field
// would make PUT /v1/world/pipeline start reading a project out of the body — and
// scope() 400s when the ?project query disagrees with the authenticated claim, so a
// body that named ANY project would begin failing a write that succeeds today. The
// project is also a TENANT key, which an In field must never carry: an In field is
// caller-supplied.
func TestProjectIsNeverAnInputField(t *testing.T) {
	app, _ := mountWorld(t)

	// A body naming a stranger's project is ignored, and the write lands in the
	// caller's own project.
	code, raw := do(t, app, http.MethodPut, "/v1/world/pipeline", "acme", "u1", "mine",
		map[string]any{"project": "stranger", "org": "other", "feeds": []string{}})
	if code != http.StatusOK {
		t.Fatalf("put pipeline: want 200, got %d (%s)", code, raw)
	}
	var got pipelineView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Org != "acme" || got.Project != "mine" {
		t.Fatalf("a body redirected the tenant: org=%q project=%q — both must come from the "+
			"validated principal", got.Org, got.Project)
	}
}

// TestProjectQueryStillCrossChecks proves the ?project guard survived the
// conversion: a query that disagrees with the authenticated project claim is 400,
// and one that agrees is served. A client cannot widen its own scope with a query.
func TestProjectQueryStillCrossChecks(t *testing.T) {
	app, _ := mountWorld(t)

	if code, raw := do(t, app, http.MethodGet, "/v1/world/pipeline?project=other", "acme", "u1", "mine", nil); code != http.StatusBadRequest {
		t.Fatalf("mismatched ?project: want 400, got %d (%s)", code, raw)
	}
	if code, raw := do(t, app, http.MethodGet, "/v1/world/pipeline?project=mine", "acme", "u1", "mine", nil); code != http.StatusOK {
		t.Fatalf("matching ?project: want 200, got %d (%s)", code, raw)
	}
}

// TestScopedOpsFailClosedWithoutAPrincipal proves every typed op refuses when there
// is no validated principal — the same 403 the untyped handlers gave — including on
// the write, whose body decodes fine.
func TestScopedOpsFailClosedWithoutAPrincipal(t *testing.T) {
	app, _ := mountWorld(t)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/world/news", nil},
		{http.MethodGet, "/v1/world/pipeline", nil},
		{http.MethodPut, "/v1/world/pipeline", map[string]any{"feeds": []string{}}},
	} {
		if code, raw := do(t, app, c.method, c.path, "", "", "", c.body); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal: want 403, got %d (%s)", c.method, c.path, code, raw)
		}
	}
}

// TestLimitsNeedsNoPrincipal pins that the plan echo stayed UNGATED — it reports a
// catalog contract and carries no tenant data, and typing it must not have added an
// identity requirement.
func TestLimitsNeedsNoPrincipal(t *testing.T) {
	app, _ := mountWorld(t)
	code, raw := do(t, app, http.MethodGet, "/v1/world/limits", "", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("limits with no principal: want 200, got %d (%s)", code, raw)
	}
	var got limitsView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Plan != "world-free" || got.Unit != "requests/minute" {
		t.Fatalf("empty plan must resolve the free floor: %+v", got)
	}
	if got.Limits.ModelAPI {
		t.Fatal("the free floor must not grant the model API")
	}
}

// TestStreamIsStillReachable proves the SSE route is untouched by the conversion —
// it is the one operation here that cannot be a typed op, and it must still be
// gated and still be mounted.
func TestStreamIsStillReachable(t *testing.T) {
	app, _ := mountWorld(t)
	if code, raw := do(t, app, http.MethodGet, "/v1/world/stream", "", "", "", nil); code != http.StatusForbidden {
		t.Fatalf("stream with no principal: want 403 (mounted and gated), got %d (%s)", code, raw)
	}
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
	"GET /v1/world/stream": "Server-Sent Events. A typed op returns ONE value that zip marshals and " +
		"writes as the whole response; this route holds the connection open writing frame after frame " +
		"through c.SendStreamWriter (stream.go) until the client goes away, bounded only by a 25s " +
		"heartbeat. There is no Out that can express a stream.",
}

// worldOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the REAL mount, not a reconstruction of it, is what makes this a gate.
func worldOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountWorldOnly(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "world", Version: "v1"})
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
	served, typed := worldOps(t)

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
			t.Errorf("untypedByDesign names %q, which world no longer serves", key)
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
	_, typed := worldOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed world ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/world/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate cannot
// see. A typed op publishes its Out's whole schema, and a property that reaches
// openapi.yaml with no description reaches every generated SDK and every MCP inputSchema
// without one too.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountWorldOnly(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "world", Version: "v1"})
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
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/world/...",
			len(bare), strings.Join(bare, ", "))
	}
}
