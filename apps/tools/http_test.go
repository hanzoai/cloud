package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// newApp resets the process-wide registry to a fresh one, mounts the tools plane on
// a fresh app, and lets a test register extra /v1 routes (for the builtin source).
func newApp(t *testing.T, extra func(*zip.App)) *zip.App {
	t.Helper()
	old := std
	std = NewRegistry()
	t.Cleanup(func() { std = old })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if extra != nil {
		extra(app)
	}
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

type result struct {
	Code int
	Body []byte
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) result {
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
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Body: b}
}

func rpc(t *testing.T, app *zip.App, org, raw string) result {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/tools/mcp", bytes.NewReader([]byte(raw)))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Body: b}
}

// TestMCPGate403: the unified MCP endpoint refuses a caller with no validated
// principal — the tool plane never serves an unauthenticated request.
func TestMCPGate403(t *testing.T) {
	app := newApp(t, nil)
	r := rpc(t, app, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if r.Code != 403 {
		t.Fatalf("mcp without principal want 403, got %d (%s)", r.Code, r.Body)
	}
}

// TestActivationAndMCPCall: the full activation round-trip. A registered source's
// tool is invisible + un-callable until activated via PUT /v1/tools/activation;
// once activated it appears in tools/list and tools/call dispatches; an unactivated
// tool is refused 403.
func TestActivationAndMCPCall(t *testing.T) {
	app := newApp(t, nil)
	std.Register(&fakeProvider{src: SourceConnector, tools: []Tool{
		tool("acme_hello", SourceConnector),
		tool("acme_secret", SourceConnector),
	}})

	// Before activation: tools/list is empty, tools/call is 403.
	list := rpc(t, app, "acme", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if names := toolNames(t, list.Body); len(names) != 0 {
		t.Fatalf("pre-activation tools/list must be empty, got %v", names)
	}
	call := rpc(t, app, "acme", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"acme_hello","arguments":{}}}`)
	if call.Code != 403 {
		t.Fatalf("unactivated tools/call want 403, got %d (%s)", call.Code, call.Body)
	}

	// Activate one tool via the activation API.
	act := do(t, app, http.MethodPut, "/v1/tools/activation", "acme", map[string]any{"activate": []string{"acme_hello"}})
	if act.Code != 200 {
		t.Fatalf("activate want 200, got %d (%s)", act.Code, act.Body)
	}
	// GET reflects it.
	get := do(t, app, http.MethodGet, "/v1/tools/activation", "acme", nil)
	if !bytes.Contains(get.Body, []byte("acme_hello")) {
		t.Fatalf("activation list must contain acme_hello, got %s", get.Body)
	}

	// tools/list now shows ONLY the activated tool.
	list = rpc(t, app, "acme", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	names := toolNames(t, list.Body)
	if len(names) != 1 || names[0] != "acme_hello" {
		t.Fatalf("post-activation tools/list must be [acme_hello], got %v", names)
	}

	// tools/call dispatches the activated tool.
	call = rpc(t, app, "acme", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"acme_hello","arguments":{}}}`)
	if call.Code != 200 || !bytes.Contains(call.Body, []byte(`\"by\":\"connector\"`)) {
		t.Fatalf("activated tools/call must dispatch on connector, got %d (%s)", call.Code, call.Body)
	}

	// The still-unactivated sibling stays 403 (activation is per-tool).
	call = rpc(t, app, "acme", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"acme_secret","arguments":{}}}`)
	if call.Code != 403 {
		t.Fatalf("sibling unactivated tools/call want 403, got %d (%s)", call.Code, call.Body)
	}
}

// TestExternalMCPDispatch: an org's registered EXTERNAL MCP server surfaces its
// tools through the registry and dispatches through the per-principal plane. Uses a
// local JSON-RPC MCP server with an injected plain client (the SSRF guard, tested
// separately, blocks loopback in production).
func TestExternalMCPDispatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Assert the auth header the registry injected from KMS is present.
		if r.Header.Get("Authorization") != "Bearer sekret" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"tools": []map[string]any{{"name": "echo", "description": "echoes input", "inputSchema": map[string]any{"type": "object"}}},
			}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"tool": req.Params.Name, "echoed": req.Params.Arguments,
			}})
		default:
			http.Error(w, "bad method", http.StatusBadRequest)
		}
	}))
	defer ts.Close()

	store, err := OpenMCPServerStore(t.TempDir() + "/mcp.db")
	if err != nil {
		t.Fatalf("OpenMCPServerStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Create(context.Background(), MCPServer{
		ID: "m123abc", Org: "acme", Name: "myserver", URL: ts.URL,
		AuthHeader: "Authorization", HasSecret: true,
	}); err != nil {
		t.Fatalf("create server: %v", err)
	}

	p := newMCPProvider(store, fakeKMS{"m123abc": "Bearer sekret"})
	p.http = ts.Client() // bypass the SSRF-guarded dialer for the loopback test server.

	// List surfaces the remote tool, prefixed by server id.
	tools, err := p.List(context.Background(), Scope{Org: "acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "m123abc_echo" || tools[0].Source != SourceMCP {
		t.Fatalf("List must surface m123abc_echo, got %+v", tools)
	}

	// Dispatch routes to the server, injects the KMS auth, and returns the result.
	out, err := p.Dispatch(context.Background(), Principal{Org: "acme"}, "m123abc_echo", map[string]any{"hi": "there"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	b, _ := json.Marshal(out)
	if !bytes.Contains(b, []byte(`"tool":"echo"`)) || !bytes.Contains(b, []byte(`"hi":"there"`)) {
		t.Fatalf("external dispatch result mismatch: %s", b)
	}

	// Cross-org isolation: org "evil" has no such server → unknown tool.
	if _, err := p.Dispatch(context.Background(), Principal{Org: "evil"}, "m123abc_echo", nil); err == nil {
		t.Fatalf("cross-org external dispatch must fail")
	}
}

// TestBuiltinRouteTool: full-cloud-control — an arbitrary /v1 route becomes a tool
// and dispatches IN-PROCESS through the same Fiber app, returning its response.
func TestBuiltinRouteTool(t *testing.T) {
	app := newApp(t, func(a *zip.App) {
		a.Get("/v1/ping", func(c *zip.Ctx) error {
			return c.JSON(http.StatusOK, map[string]any{"pong": true})
		})
	})
	// The route surfaces as a builtin tool.
	p := newBuiltinProvider(app)
	tools, _ := p.List(context.Background(), Scope{Org: "acme"})
	found := false
	for _, tl := range tools {
		if tl.Name == "cloud_get_ping" {
			found = true
		}
	}
	if !found {
		t.Fatalf("builtin must expose cloud_get_ping, got %d tools", len(tools))
	}
	// Activate + dispatch through the FULL registry+HTTP path.
	do(t, app, http.MethodPut, "/v1/tools/activation", "acme", map[string]any{"activate": []string{"cloud_get_ping"}})
	call := rpc(t, app, "acme", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cloud_get_ping","arguments":{}}}`)
	if call.Code != 200 || !bytes.Contains(call.Body, []byte(`\"pong\":true`)) {
		t.Fatalf("builtin dispatch must return the route response, got %d (%s)", call.Code, call.Body)
	}
}

// TestSSRFGuard: the registration boundary rejects non-public / metadata targets.
func TestSSRFGuard(t *testing.T) {
	bad := []string{
		"http://localhost/mcp", "http://127.0.0.1/mcp", "http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5/mcp", "http://192.168.1.1/mcp", "ftp://example.com/mcp", "http:///nohost",
	}
	for _, u := range bad {
		if err := validateServerURL(u); err == nil {
			t.Fatalf("validateServerURL must reject %q", u)
		}
	}
	if err := validateServerURL("https://mcp.example.com/rpc"); err != nil {
		t.Fatalf("validateServerURL must accept a public https url, got %v", err)
	}
	if isPublicIP(net.ParseIP("169.254.169.254")) || isPublicIP(net.ParseIP("10.1.2.3")) || isPublicIP(net.ParseIP("::1")) {
		t.Fatalf("isPublicIP must reject metadata/private/loopback")
	}
	if !isPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatalf("isPublicIP must accept a public address")
	}
}

// ── test doubles ────────────────────────────────────────────────────────────────

// fakeKMS maps an mcp server id's authRef to a secret value.
type fakeKMS map[string]string

func (f fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	for id, v := range f {
		if ref == authRef("acme", id) {
			return []byte(v), nil
		}
	}
	return nil, io.EOF
}
func (f fakeKMS) PutSecret(_ context.Context, _ string, _ []byte) error { return nil }
func (f fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, io.EOF
}

func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode tools/list: %v (%s)", err, body)
	}
	names := make([]string, 0, len(out.Result.Tools))
	for _, tl := range out.Result.Tools {
		names = append(names, tl.Name)
	}
	return names
}
