package visor

// The point of a typed op is that ONE registration feeds four surfaces. This
// asserts the three derived ones actually carry this subsystem — a route that
// silently regressed to app.Get would still serve REST and would vanish from all
// of them, which no other test here would notice.

import (
	"encoding/json"
	"net/http/httptest"
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

	get, ok := doc.Paths["/v1/machines/{id}"]["get"]
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
	del := doc.Paths["/v1/machines/{id}"]["delete"]
	if _, ok := del.Responses["204"]; !ok {
		t.Errorf("DELETE /v1/machines/{id} responses = %v, want a 204", del.Responses)
	}

	// A creator states its request schema, and the example comes from the comment.
	if _, ok := doc.Components.Schemas["createClusterReq"]; !ok {
		t.Errorf("createClusterReq schema missing: %v", keys(doc.Components.Schemas))
	}
	if !strings.Contains(spec, `"gpu-h100x8-640gb"`) {
		t.Errorf("the createK8sCluster example did not reach the spec")
	}

	// The routes that stay raw are honestly absent — they carry no shape, so the
	// document must not claim one for them.
	for _, p := range []string{"/v1/compute/regions", "/v1/compute/sizes", "/v1/compute/bots/launch"} {
		if _, ok := doc.Paths[p]; ok {
			t.Errorf("%s is raw by design but appears in the document", p)
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

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
