package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// ONE typed op, every projection. This is the test that makes the rest of the
// typing migration mechanical rather than exploratory: it shows what a route
// GAINS by being declared with zip.Post[In,Out] instead of app.Post, and it
// fails if any one of those surfaces silently stops being derived.
//
// An untyped route has exactly one of these — the HTTP route. It is in no
// document, is no agent tool, has no command, and cannot be reached by name from
// another service. That is the whole cost, and it is why ~1000 routes are worth
// converting.
func TestTargetOpsProjectEverywhere(t *testing.T) {
	app := mountApp(t, nil)

	const opID = "post_v1_agents_targets"
	const path = "/v1/agents/targets"

	// ---- 1. OpenAPI: the document the SDK repos generate from -------------
	spec := app.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no %s in the document — the op is not projected at all", path)
	}
	post, _ := item["post"].(map[string]any)
	if post["operationId"] != opID {
		t.Errorf("operationId = %v, want %q", post["operationId"], opID)
	}
	// The doc comment reaches the document. Prose is product surface: it is what
	// a human reads in the reference and what a model reads to pick a tool.
	if d, _ := post["description"].(string); !strings.Contains(d, "re-links one that is") {
		t.Errorf("description does not carry the handler's doc comment: %q", d)
	}
	// The In type reaches it as a schema, by $ref — one definition, shared.
	body, _ := post["requestBody"].(map[string]any)
	content, _ := body["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	sch, _ := media["schema"].(map[string]any)
	if ref, _ := sch["$ref"].(string); ref != "#/components/schemas/targetReq" {
		t.Errorf("requestBody schema = %v, want a $ref to targetReq", sch)
	}
	// …and the doc comment's Example rides with it, so the reference is
	// pressable rather than merely readable.
	if ex, ok := media["example"]; !ok {
		t.Error("no example on the request body")
	} else if b, _ := json.Marshal(ex); !strings.Contains(string(b), "gpu-01") {
		t.Errorf("example = %s, want the doc comment's own", b)
	}

	// ---- 2. MCP: the same op as a tool an agent can call ------------------
	var tool map[string]any
	for _, x := range app.MCPTools() {
		if x["name"] == opID {
			tool = x
		}
	}
	if tool == nil {
		t.Fatalf("no MCP tool named %q — the op is invisible to agents", opID)
	}
	if d, _ := tool["description"].(string); !strings.Contains(d, "re-links one that is") {
		t.Errorf("tool description does not carry the doc comment: %q", d)
	}
	in, _ := tool["inputSchema"].(map[string]any)
	props, _ := in["properties"].(map[string]any)
	for _, f := range []string{"label", "kind", "host"} {
		if _, ok := props[f]; !ok {
			t.Errorf("tool inputSchema has no %q; properties = %v", f, keysOfAny(props))
		}
	}

	// ---- 3. CLI: the same op as a command ---------------------------------
	var cmd struct {
		service, name string
		flags         []string
	}
	for _, c := range app.Commands() {
		if c.OperationID == opID {
			cmd.service, cmd.name = c.Service, c.Name
			for _, f := range c.Flags {
				cmd.flags = append(cmd.flags, f.Name)
			}
		}
	}
	if cmd.service != "agents" || cmd.name != "targets-create" {
		t.Errorf("command = %q %q, want %q %q", cmd.service, cmd.name, "agents", "targets-create")
	}
	if !slices.Contains(cmd.flags, "label") || !slices.Contains(cmd.flags, "host") {
		t.Errorf("command flags = %v, want the In fields", cmd.flags)
	}

	// ---- 4. The op-call plane: reachable BY NAME from another service -----
	// Same operation id, which is the whole point: one token addresses this op
	// through the document, the tool list, the command line and zip.Call alike.
	if post["operationId"] != tool["name"] {
		t.Errorf("document says %v, tool says %v — one op must have one name",
			post["operationId"], tool["name"])
	}
}

// The DELETE proves the v1.18 wire: a bodyless method takes its input from the
// URL, and the document says so rather than describing a body no client sends.
func TestTargetDeleteIsURLOnly(t *testing.T) {
	app := mountApp(t, nil)
	paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	del, _ := paths["/v1/agents/targets/{id}"]["delete"].(map[string]any)
	if del == nil {
		t.Fatal("no DELETE /v1/agents/targets/{id} in the document")
	}
	if _, has := del["requestBody"]; has {
		t.Error("DELETE declares a requestBody — a bodyless method carries none")
	}
	params, _ := del["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("parameters = %v, want the one path param", params)
	}
	p, _ := params[0].(map[string]any)
	if p["name"] != "id" || p["in"] != "path" || p["required"] != true {
		t.Errorf("parameter = %v, want a required path param named id", p)
	}
}

func keysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
