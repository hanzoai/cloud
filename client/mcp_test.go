// Copyright © 2026 Hanzo AI. MIT License.

package client_test

// The MCP server, end to end, over the wire it actually uses.
//
// Every test here starts REAL child processes' worth of machinery — a zip app
// per subsystem, listening on its own ZAP unix socket, exactly as a plugin child
// does — and drives the composed MCP server with JSON-RPC bodies. Nothing is
// stubbed at the client being tested, because the client being tested is the
// client that was wrong: the old MCP server answered from a committed array and
// every test of it passed while the array was missing 353 of o11y's ops.
//
// So the assertions are BY BODY and never by status code, and they are EXACT
// SETS: "the MCP server lists what the children serve" is only a real claim if
// dropping a child from the MCP server turns it red. See
// TestEndpointListsExactlyWhatItsChildrenServe.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sock"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// child is one subsystem: a zip app with its own typed ops, serving its own MCP
// endpoint on its own socket — the shape cloud.Serve gives every plugin binary.
type child struct {
	name string
	addr string
	app  *zip.App
}

type thingIn struct {
	Which string `json:"which"`
}
type thingOut struct {
	App   string `json:"app"`
	Which string `json:"which"`
}

// start brings up one child with n typed ops named "<app>_op<i>", and returns it
// once its socket accepts.
func start(t *testing.T, name string, ops int) *child {
	t.Helper()
	sock := filepath.Join(sock.Dir(t), name+".sock")
	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	for i := range ops {
		id := opID(name, i)
		zip.Post(app, "/v1/"+name+"/"+id, func(_ context.Context, in *thingIn) (*thingOut, error) {
			return &thingOut{App: name, Which: in.Which}, nil
		}, zip.WithOperationID(id), zip.WithSummary("what "+name+" does at "+id))
	}
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, sock)
	return &child{name: name, addr: sock, app: app}
}

func opID(app string, i int) string { return app + "_op" + string(rune('a'+i)) }

// die stops a child the way zip stops one: the process goes, and its PRIVATE
// directory goes with it (zip load.go stop → os.RemoveAll(in.dir)), so the socket
// a caller would reach for is not there any more. Reproducing only the first half
// — closing the listener while leaving the path dialable through an already
// pooled connection — is a fixture that tests nothing, because the in-process
// server keeps answering on it.
func die(t *testing.T, k *child) {
	t.Helper()
	if err := k.app.Shutdown(); err != nil {
		t.Fatalf("stop %s: %v", k.name, err)
	}
	k.addr = k.addr + ".gone"
}

func waitFor(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never accepted", sock)
}

// host composes an MCP server over the named children. at answers from the map,
// so a name with no child is an app this host cannot reach — which is exactly the
// "deliberately stopped" case.
func host(t *testing.T, apps []string, kids map[string]*child) *zip.App {
	t.Helper()
	h := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	d := client.Use(h, "/v1/mcp", apps, func(app string) (addr, path string, err error) {
		k := kids[app]
		if k == nil {
			return "", "", &net.AddrError{Err: "no instance running", Addr: app}
		}
		return k.addr, manifest.FrameworkMCPPath, nil
	})
	// The MCP server lists what a subsystem PUBLISHED and asks nothing, so a
	// fixture has to say what these children publish. They are real apps, so their
	// own registries are the publication — the same value plugin/gen-client-catalog
	// reads out of each app's openapi.json for the embedded catalog.
	d.Catalog = func(app string) []client.Op {
		k := kids[app]
		if k == nil {
			return nil
		}
		var ops []client.Op
		for _, tl := range k.app.MCPTools() {
			name, _ := tl["name"].(string)
			desc, _ := tl["description"].(string)
			ops = append(ops, client.Op{ID: name, Doc: desc})
		}
		return ops
	}
	return h
}

// rpc posts one JSON-RPC message to the MCP server and returns the decoded result.
func rpc(t *testing.T, h *zip.App, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("POST", "http://cloud/v1/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A bearer, because the edge challenges a tools/call that carries none
	// (challenge_test.go); these tests are about dispatch, which starts after that.
	req.Header.Set("Authorization", "Bearer test")
	resp, err := h.Test(req, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST /v1/mcp: %v", err)
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
		return map[string]any{"error": *env.Error}
	}
	return env.Result
}

// offered is every OPERATION the MCP server offers, in the order it offers them.
//
// The MCP server publishes one tool per subsystem and carries the operations in
// that tool's `op` enum (client/grouped.go), so the operations are read out of the
// enums rather than off the tool names. That is the same question these tests
// always asked — "what can be called through this MCP server" — put to the
// surface that now answers it. describe has no enum and contributes nothing.
func offered(res map[string]any) []string {
	var out []string
	for _, tl := range published(res) {
		m, _ := tl.(map[string]any)
		schema, _ := m["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		op, _ := props["op"].(map[string]any)
		enum, _ := op["enum"].([]any)
		for _, n := range enum {
			s, _ := n.(string)
			out = append(out, s)
		}
	}
	return out
}

// published is the TOOLS the MCP server publishes — the per-subsystem envelopes
// themselves, not the operations inside them.
func published(res map[string]any) []any {
	tools, _ := res["tools"].([]any)
	return tools
}

// names lifts the tool names out of published().
func names(res map[string]any) []string {
	tools := published(res)
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		n, _ := m["name"].(string)
		out = append(out, n)
	}
	return out
}

// listed is the operation names the MCP server offers, sorted.
func listed(t *testing.T, h *zip.App) []string {
	t.Helper()
	out := offered(rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	sort.Strings(out)
	return out
}

// unavailable is the subsystems tools/list says it could not ask.
func unavailable(t *testing.T, h *zip.App) map[string]string {
	t.Helper()
	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	meta, _ := res["_meta"].(map[string]any)
	rows, _ := meta[client.Unavailable].([]any)
	out := map[string]string{}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		app, _ := m["app"].(string)
		why, _ := m["error"].(string)
		out[app] = why
	}
	return out
}

// TestEndpointListsExactlyWhatItsChildrenServe is the whole claim, as an EXACT set.
//
// It is exact on purpose. A subset assertion ("o11y's tools are in there") is the
// test the deleted catalogue passed for months while it was missing 353 ops: a
// list that is too short satisfies every containment check written against it.
// Drop a child from `apps` below and this goes red naming the tools that vanished
// — which is the mutation that proves the suite is load-bearing.
func TestEndpointListsExactlyWhatItsChildrenServe(t *testing.T) {
	kids := map[string]*child{"alpha": start(t, "alpha", 3), "beta": start(t, "beta", 2)}
	h := host(t, []string{"alpha", "beta"}, kids)

	want := []string{"alpha_opa", "alpha_opb", "alpha_opc", "beta_opa", "beta_opb"}
	got := listed(t, h)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the MCP server lists %v, the children serve %v", got, want)
	}
	if u := unavailable(t, h); len(u) != 0 {
		t.Fatalf("every child answered, so nothing may be reported unavailable: %v", u)
	}
}

// TestEndpointAnswersTheChildsOwnProjection: the descriptors are the child's bytes,
// not a re-encoding and not a copy that could differ from them.
//
// This is the property the file could never have: the host's answer for an app is
// EQUAL to what that app's own registry projects, at the instant of asking, so
// there is no version of the surface in which they disagree.
func TestEndpointAnswersTheChildsOwnProjection(t *testing.T) {
	kid := start(t, "alpha", 4)
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": kid})

	var want []string
	for _, tl := range kid.app.MCPTools() {
		want = append(want, tl["name"].(string))
	}
	sort.Strings(want)
	if got := listed(t, h); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the MCP server lists %v; alpha's own MCPTools() is %v", got, want)
	}
}

// TestToolsCallReachesTheOwnersOwnHandler: the tool RUNS, in the child that
// declared it, and the child's own reply comes back verbatim.
func TestToolsCallReachesTheOwnersOwnHandler(t *testing.T) {
	kids := map[string]*child{"alpha": start(t, "alpha", 2), "beta": start(t, "beta", 2)}
	h := host(t, []string{"alpha", "beta"}, kids)

	// No tools/list first: a call must be able to find its owner by asking, or the
	// MCP server only works for a client that listed in the same process lifetime.
	res := rpc(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"beta_opb","arguments":{"which":"x"}}}`)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call beta_opb returned no content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, `"app":"beta"`) || !strings.Contains(text, `"which":"x"`) {
		t.Fatalf("tools/call ran somewhere else or lost its arguments: %q", text)
	}
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("tools/call reported an error: %v", res)
	}
}

// TestToolsCallOfANameNobodyServesIsRefused: -32602 from the host, after asking.
// It is a REFUSAL and not an outage: every child answered and none claims it.
func TestToolsCallOfANameNobodyServesIsRefused(t *testing.T) {
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": start(t, "alpha", 1)})
	res := rpc(t, h, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ghost_op","arguments":{}}}`)
	e, ok := res["error"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown tool must be refused, got %v", res)
	}
	if code, _ := e["code"].(float64); int(code) != -32602 {
		t.Errorf("code = %v, want -32602", e["code"])
	}
}

// TestToolsCallOnADownOwnerIsIsErrorNotATransportFailure: per the MCP spec the
// model must be able to READ the failure, so a dead hop is isError content.
func TestToolsCallOnADownOwnerIsIsErrorNotATransportFailure(t *testing.T) {
	kids := map[string]*child{"alpha": start(t, "alpha", 1), "beta": start(t, "beta", 1)}
	h := host(t, []string{"alpha", "beta"}, kids)
	if got := listed(t, h); len(got) != 2 { // learn the owners while beta is up
		t.Fatalf("want 2 tools, got %v", got)
	}
	die(t, kids["beta"])
	res := rpc(t, h, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"beta_opa","arguments":{}}}`)
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("a call to a stopped owner must come back as isError content, got %v", res)
	}
}

// TestInitializeAndPingAnswerWithoutTouchingAChild: the handshake is the host's,
// and a client that only initializes must not wake the surface.
func TestInitializeAndPingAnswerWithoutTouchingAChild(t *testing.T) {
	h := host(t, []string{"alpha"}, map[string]*child{}) // no child exists at all
	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if res["protocolVersion"] == nil {
		t.Fatalf("initialize did not answer a protocolVersion: %v", res)
	}
	if res := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); res == nil {
		t.Fatal("ping did not answer")
	}
}

// TestALISTIsAFactOfTHERELEASENotOfWhoIsUp is the contract that replaced four
// tests, and the replacement is a narrowing rather than a loosening.
//
// Those four pinned the MCP server NAMING a subsystem it could not reach: it
// asked every running child, so a child that had stopped produced an error and
// the answer carried hanzo.ai/unavailable. That reporting was real and it was
// paid for by asking, and asking is what the MCP server may no longer do — one
// list took 92 seconds against Cloudflare's 100-second ceiling and three in a row
// drove the pod past its own liveness probe until the kubelet killed it.
//
// So the two facts are separated. WHAT the surface serves is a property of the
// release: generated from each subsystem's own document at build time, identical
// on every replica, unchanged by a restart. WHETHER a subsystem is up is a
// property of this instant, and it is answered where it matters — by CALLING one.
// A list that quietly depended on liveness could not give either answer honestly.
func TestALISTIsAFactOfTHERELEASENotOfWhoIsUp(t *testing.T) {
	alpha := startNamed(t, "alpha", "opa", "opb")
	beta := startNamed(t, "beta", "opa")
	h := host(t, []string{"alpha", "beta"}, map[string]*child{"alpha": alpha, "beta": beta})

	before := order(t, h)
	_ = beta.app.Shutdown() // gone, and the release did not change

	if after := order(t, h); !slices.Equal(before, after) {
		t.Errorf("the list moved when a subsystem stopped: %v -> %v — it is supposed to "+
			"describe the release, not who happens to be up", before, after)
	}

	// And the honest answer about liveness comes from a CALL, which is the only
	// thing that has to reach the subsystem at all.
	got := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"beta","arguments":{"op":"beta_opa","input":{}}}}`)
	if got["error"] == nil && got["isError"] == nil {
		t.Errorf("calling a stopped subsystem reported success: %v", got)
	}
}
