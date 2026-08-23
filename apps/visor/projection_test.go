package visor

// The point of a typed op is that ONE registration feeds four surfaces. This
// asserts the three derived ones actually carry this subsystem — a route that
// silently regressed to app.Get would still serve REST and would vanish from all
// of them, which no other test here would notice.

import (
	"encoding/json"
	"github.com/hanzoai/cloud/openapi"
	"maps"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// projectionApp mounts the surface and installs the deferred projections without
// listening, which is how zip exposes them to a Fiber test.
func projectionApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"),
		OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"}})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func body(t *testing.T, app *zip.App, method, path, payload string) string {
	t.Helper()
	var r *strings.Reader = strings.NewReader(payload)
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 0, 1<<16)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(b)
}

// TestOpenAPICarriesTheSurface pins that the document is derived from the code:
// the route, the schema of its Out, the prose from the handler's doc comment and
// the example from that same comment all have to be there, and the doc comment is
// the only place any of it is written.
func TestOpenAPICarriesTheSurface(t *testing.T) {
	spec := body(t, projectionApp(t), "GET", "/.well-known/openapi.json", "")

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Parameters  []struct {
				Name        string `json:"name"`
				In          string `json:"in"`
				Description string `json:"description"`
			} `json:"parameters"`
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("openapi is not json: %v", err)
	}

	get, ok := doc.Paths["/v1/visor/machines/{id}"]["get"]
	if !ok {
		t.Fatalf("GET /v1/machines/{id} is not in the document")
	}
	if get.OperationID != "getMachine" {
		t.Errorf("operationId = %q, want getMachine", get.OperationID)
	}
	// The prose is the doc comment, and the summary its first sentence — neither is
	// passed to WithSummary anywhere, so their presence proves the zipdoc pass ran.
	if !strings.Contains(get.Description, "org-scoped name") {
		t.Errorf("description did not come from the handler doc comment: %q", get.Description)
	}
	// …and WITHOUT the leading `getMachine` the source comment opens with. The
	// identifier belongs to Go's namespace, not the document's: zip strips an exact
	// leading match of the handler's own name and re-capitalises (v1.18.13), so the
	// summary reads as prose to the SDK, the MCP door and the CLI that print it.
	if !strings.HasPrefix(get.Summary, "Returns one of the caller org") {
		t.Errorf("summary = %q, want the doc comment's first sentence", get.Summary)
	}
	// A templated path MUST declare its parameter, and the parameter's help is the
	// doc comment on the In field it binds to.
	if len(get.Parameters) != 1 || get.Parameters[0].Name != "id" || get.Parameters[0].In != "path" {
		t.Fatalf("path parameter not declared: %+v", get.Parameters)
	}
	if !strings.Contains(get.Parameters[0].Description, "stable key Visor addresses a") {
		t.Errorf("parameter help did not come from the In field's doc comment: %q", get.Parameters[0].Description)
	}

	// A delete states 204 and promises no body.
	del := doc.Paths["/v1/visor/machines/{id}"]["delete"]
	if _, ok := del.Responses["204"]; !ok {
		t.Errorf("DELETE /v1/machines/{id} responses = %v, want a 204", del.Responses)
	}

	// A creator states its request schema, and the example comes from the comment.
	if _, ok := doc.Components.Schemas["createClusterReq"]; !ok {
		t.Errorf("createClusterReq schema missing: %v", slices.Collect(maps.Keys(doc.Components.Schemas)))
	}
	if !strings.Contains(spec, `"gpu-h100x8-640gb"`) {
		t.Errorf("the createK8sCluster example did not reach the spec")
	}

	// The routes that stay raw are honestly absent — they carry no shape, so the
	// document must not claim one for them.
	//
	// The two CATALOG reads used to be on this list and are typed ops now. They were
	// here because their shape is Visor's rather than ours, which reads like a
	// blocker and is not: a json.RawMessage Out publishes `{}` — "any JSON", the true
	// thing to say about an upstream contract we do not own — and carries the same
	// bytes through. Modelling upstream's shape would have been the mistake; refusing
	// to publish the ADDRESS because of it was a smaller one, and cost them a tool, a
	// CLI command and an SDK method each.
	for _, p := range []string{"/v1/visor/compute/bots/launch"} {
		if _, ok := doc.Paths[p]; ok {
			t.Errorf("%s is raw by design but appears in the document", p)
		}
	}
	// And the catalog reads ARE in the document, with an open schema rather than an
	// invented one — the assertion that keeps the fix above from being reverted into
	// a modelled copy of somebody else's contract.
	for _, p := range []string{"/v1/visor/compute/regions", "/v1/visor/compute/sizes"} {
		op, ok := doc.Paths[p]["get"]
		if !ok {
			t.Errorf("%s is a typed op and must appear in the document", p)
			continue
		}
		if op.Description == "" {
			t.Errorf("%s carries no description, so it reaches no SDK and no MCP tool", p)
		}
		// The response schema is OPEN — `{}`, "any JSON" — and that is asserted on
		// the OPERATION rather than on a component, because a self-marshalling type
		// is INLINED and never enters components. An earlier version of this check
		// looked up a `catalogList` component, found nothing, and passed: an
		// assertion that passes because nothing rendered is no assertion.
		raw, _ := json.Marshal(op.Responses["200"])
		if s := string(raw); !strings.Contains(s, `"schema":{}`) {
			t.Errorf("%s publishes %s as its 200 — the payload is UPSTREAM's, so the honest schema is "+
				"an open one. A modelled shape here is a second copy of somebody else's contract, free "+
				"to drift on their next release and silently dropping any field it does not name", p, s)
		}
	}
}

// TestMCPPublishesTheSurface pins the second derived projection: the same ops,
// published as tools under the same names, with their In schema.
//
// The tool descriptions carry the zipdoc prose now — mcpToolOf reads the same
// docFor extraction the OpenAPI builder does (zip mcp.go), so one doc comment
// serves the document, the CLI help and the tool list. Nothing here passes
// WithSummary to say the same sentence twice. That prose is what the FLEET's one
// MCP door hands a model: this app's catalogue is plugin/visor/mcp.json, and
// manifest/mcp_test.go fails a tool whose description is empty.
func TestMCPPublishesTheSurface(t *testing.T) {
	tools := body(t, projectionApp(t), "POST", "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, name := range []string{"listMachines", "getMachine", "deleteMachine",
		"listFleet", "cancelFleetJob", "createKubernetesCluster", "listBots"} {
		if !strings.Contains(tools, `"`+name+`"`) {
			t.Errorf("MCP tools/list is missing %s", name)
		}
	}
	// The raw routes are absent here too — same registry, same consequence.
	if strings.Contains(tools, "launchBot") {
		t.Errorf("launchBot is raw by design but appears as an MCP tool")
	}
}

// untypedByDesign is the CLOSED list of visor addresses that stay raw handlers.
//
// The three refusals were already written at their registrations, and
// TestOpenAPICarriesTheSurface already asserted that each is ABSENT from the
// document. What neither did is SUM: a fourth raw route added tomorrow is absent
// from the document too, so the existing check passes for it, and nothing here
// notices. openapi/untyped.json catches that fleet-wide by count — and a count is
// flat when one route converts while another arrives raw in the same change, which
// is precisely what this catches.
//
// It said FIVE until the two compute-catalog reads became typed ops. Their reason,
// "the shape is Visor's and not this package's", is a true fact that argues against
// MODELLING upstream's shape and says nothing about publishing the address.
var untypedByDesign = map[string]string{
	"POST /v1/visor/machines": "TWO success shapes on one address: 201 with the machine, or — with " +
		"`dryRun: true` — 200 with the upstream PRICE QUOTE passed through verbatim, nothing launched " +
		"and nothing spent. An op declares one Out, and this one SPENDS REAL MONEY, so collapsing the " +
		"two would mean a caller could not tell a quote from a launch by its shape.",
	"POST /v1/visor/compute/bots/launch": "the same dryRun quote-or-launch split as the machine " +
		"launch, and the same money.",
	"POST /v1/visor/compute/bots/{id}/{action}": "a verb dispatch that streams the agent's answer " +
		"back VERBATIM; a typed op answers one marshalled value.",
}

// TestEveryRouteIsTypedOrNamed is the term the list above was missing.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := projectionApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "visor", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/visor") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/visor") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which visor no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
