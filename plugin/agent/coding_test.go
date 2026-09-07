package main

// coding_test.go — the coding op AS A MODEL MEETS IT.
//
// The claim this file exists to hold is not "the route works". It is that a
// chat turn can REACH a sandbox because the brain chose to, which is three
// separate facts and only one of them is about the route:
//
//	OFFERED     the fleet's own MCP server projects it, under a name a model
//	            calls
//	LEGIBLE     what the MCP server hands the model says what the thing does
//	FAIL-CLOSED the run's tenant and its person are read off the caller, and
//	            the input has no field either could arrive in
//
// Nothing here is a fixture at the client under test. The first two drive the
// REAL client.MCP over a child carrying the REAL registration [codingEndpoint], so
// deleting that registration fails them — which is the whole point, because the
// magic-word path that used to reach the engine has been deleted and this is
// now the only way in. A harness that rebuilt the route table by hand could
// stay green through exactly the mutation that matters.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// tool is the name the fleet's MCP server publishes the coding op under. A model
// never sees `post_agents_coding`: the MCP server renames a derived operation id
// to the verb phrase it already contains (client/verbs.go), and THIS is the
// string a tools/call carries.
//
// It was `create_coding` while the run answered at /v1/coding. The address folded
// under the app that runs it and the name followed, because the name IS the path:
// a fold moves the tool a model calls as surely as it moves the URL, and that is
// the half a router test cannot see.
const tool = "create_agent_coding"

// agentsChild brings up the agents app's coding surface on its own socket, the
// way cloud.Serve brings up a plugin binary — and it registers the route by
// CALLING codingEndpoint, so there is one registration in the program and the test
// is downstream of it rather than beside it.
func agentsChild(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := dir + "/agents.sock"

	app := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	codingEndpoint(app)
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			return sock
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never accepted", sock)
	return ""
}

// endpoint composes the real fleet MCP server over that child — client.Use, the
// same call cmd/cloud makes, with the same MCP path.
func endpoint(t *testing.T) *zip.App {
	t.Helper()
	sock := agentsChild(t)
	h := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	client.Use(h, manifest.MCPPath, []string{"agent"}, func(app string) (addr, path string, err error) {
		if app != "agent" {
			return "", "", &net.AddrError{Err: "no instance running", Addr: app}
		}
		return sock, manifest.FrameworkMCPPath, nil
	})
	return h
}

// rpc puts one JSON-RPC message to the MCP server and returns the decoded
// result.
func rpc(t *testing.T, h *zip.App, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("POST", "http://cloud"+manifest.MCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Test(req, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", manifest.MCPPath, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Result map[string]any  `json:"result"`
		Error  *map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("the MCP server answered %d with %q, which is not a JSON-RPC envelope", resp.StatusCode, raw)
	}
	if env.Error != nil {
		t.Fatalf("the MCP server refused: %v", *env.Error)
	}
	return env.Result
}

// ops reads the operation names out of one subsystem tool's schema — the `op`
// enum, which is where the grouped MCP server carries them (client/grouped.go).
// This is the same read apps/agents/endpoint.go does to build a run's offer, so what
// this asserts about is exactly what an agent is handed.
func ops(t *testing.T, res map[string]any, subsystem string) []string {
	t.Helper()
	list, _ := res["tools"].([]any)
	for _, raw := range list {
		tl, _ := raw.(map[string]any)
		if name, _ := tl["name"].(string); name != subsystem {
			continue
		}
		schema, _ := tl["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		op, _ := props["op"].(map[string]any)
		enum, _ := op["enum"].([]any)
		out := make([]string, 0, len(enum))
		for _, e := range enum {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	t.Fatalf("the MCP server published no %q tool at all; it listed %v", subsystem, list)
	return nil
}

// TestTheCodingToolIsOfferedToAnAgent is the fact the whole change rests on: an
// agent can reach a sandbox without anybody typing a magic word, because the
// fleet's MCP server offers the coding op as a tool and the default assistant
// declares the whole MCP server (apps/agents builtinAgent, ToolsAll).
//
// It goes through the MCP server rather than reading the registry directly
// because the MCP server is where the two things that could silently withhold it
// live: fleet's curation rule refuses a tool whose name discloses a secret or
// mutates an authority object, and the grouping projects one tool per subsystem
// with the operations in an enum. A registration that survives neither is
// registered and unreachable, which is indistinguishable from this test's
// absence.
func TestTheCodingToolIsOfferedToAnAgent(t *testing.T) {
	res := rpc(t, endpoint(t), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	got := ops(t, res, "agent")
	for _, op := range got {
		if op == tool {
			return
		}
	}
	t.Fatalf("the fleet MCP server offers no %q; a chat turn cannot reach a sandbox. It offered %v", tool, got)
}

// TestTheDescriptionTellsAModelWhatItDoes. The description IS the product
// surface here — it is what the MCP server hands back for this operation and the
// only thing a model reads before deciding — so it is asserted, not assumed.
//
// It is asserted for the second time, too. This op shipped for months describing
// itself as "Is the app's endpoint. It answers 202 with the run's handle…", because
// zipdoc strips an exact leading match of the handler's own name and the comment
// opened "httpCodingStart is the app's endpoint". Every word of that is true and none
// of it says the thing runs a coding task on a repository — a model reading it
// has no reason to pick it for "fix the bug in X", which is why the prefix nobody
// could delete looked load-bearing.
func TestTheDescriptionTellsAModelWhatItDoes(t *testing.T) {
	res := rpc(t, endpoint(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+client.Describe+`","arguments":{"op":"`+tool+`"}}}`)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s answered nothing for %s", client.Describe, tool)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var d struct {
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		t.Fatalf("%s did not answer a descriptor: %q", client.Describe, text)
	}
	// The words a model matches a coding request against. Not a style check: each
	// is the noun or verb that makes this operation the answer to "change the code
	// in <repo>" rather than to some other request.
	for _, word := range []string{"repository", "sandbox", "branch", "code"} {
		if !strings.Contains(strings.ToLower(d.Description), word) {
			t.Errorf("the description never says %q, so a model has no reason to choose it:\n%s", word, d.Description)
		}
	}
	if strings.HasPrefix(d.Description, "Is ") {
		t.Errorf("the description opens by describing the handler, not the act: %q", d.Description)
	}
	// A schema is what the model fills in after it has chosen. An operation the
	// MCP server describes with no shape is one it cannot call.
	if !strings.Contains(string(d.InputSchema), `"prompt"`) || !strings.Contains(string(d.InputSchema), `"repo"`) {
		t.Errorf("the descriptor's schema names neither the repo nor the task: %s", d.InputSchema)
	}
}

// TestNothingInTheInputCanNameWhoTheRunIsFor is the structural half of the
// boundary, and it is the half worth having: a check can be forgotten, a field
// that does not exist cannot be. The tenant whose balance a run spends and the
// person it is attributed to are BOTH read off the caller, so neither may appear
// on a type a model writes.
//
// It walks the type rather than listing the fields it does not want, so a field
// added tomorrow is judged by the same rule.
func TestNothingInTheInputCanNameWhoTheRunIsFor(t *testing.T) {
	forbidden := map[string]bool{
		"org": true, "owner": true, "tenant": true, // whose balance
		"subject": true, "actor": true, "user": true, "userid": true, // whose name
		"token": true, "credtoken": true, "cred": true, "key": true, // whose credential
	}
	rt := reflect.TypeOf(client.CodingStartIn{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		if forbidden[name] {
			t.Errorf("CodingStartIn.%s lets the caller state an identity; a model writes these arguments", f.Name)
		}
		tag := strings.ToLower(strings.Split(f.Tag.Get("json"), ",")[0])
		if forbidden[tag] {
			t.Errorf("CodingStartIn's %q field lets the caller state an identity on the wire", tag)
		}
	}
}

// TestARunTakesItsTenantAndItsPersonFromTheCaller drives the handler itself.
//
// Three measurements, and it takes all three: that an anonymous caller is
// refused, that a caller with an org and no person is refused, and that a
// caller carrying both gets THROUGH the identity gate to the work checks. The
// third is what makes the first two mean something — a handler that refused
// everything would pass the first two alone.
//
// The org is measured as well as its presence: a caller stating a malformed
// tenant reaches the engine's own org check, which is only possible if the
// value that travelled was the CALLER's rather than a constant.
func TestARunTakesItsTenantAndItsPersonFromTheCaller(t *testing.T) {
	work := client.CodingStartIn{Repo: "cloud", Prompt: "fix the thing"}

	for name, tc := range map[string]struct {
		ctx  context.Context
		in   client.CodingStartIn
		want string
	}{
		"anonymous": {
			context.Background(), work,
			"org required",
		},
		"a tenant with nobody behind it": {
			cloud.For(context.Background(), "acme"), work,
			"needs the person it is for",
		},
		"a malformed tenant reaches the engine's own check": {
			caller(t, "../other", "u"), work,
			"needs a tenant",
		},
		"both present, so the work is what is judged": {
			caller(t, "acme", "u"), client.CodingStartIn{Repo: "cloud"},
			"repo and task are required",
		},
	} {
		_, err := startCoding(tc.ctx, &tc.in)
		if err == nil {
			t.Errorf("%s: admitted a run it must refuse", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused with %q, want it to say %q", name, err, tc.want)
		}
	}
}

// caller states a full principal on a context with no request behind it, which
// is the one place zip reads a stated caller — the same pairing the fleet's
// agent MCP server produces when it forwards a run's own (org, actor).
func caller(t *testing.T, org, user string) context.Context {
	t.Helper()
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user})
}
