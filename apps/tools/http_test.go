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
	"github.com/zap-proto/zip"
)

// newApp resets the process-wide registry to a fresh one, mounts the tools plane on
// a fresh app, and lets a test register extra /v1 routes.
func newApp(t *testing.T, extra func(*zip.App)) *zip.App {
	t.Helper()
	old := std
	std = NewRegistry()
	t.Cleanup(func() { std = old })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// Mount the plane the way the SERVER does. A typed op receives only a context,
	// so the validated org reaches it ONLY through cloud.Bridge — which Serve
	// installs once for the whole binary, after the identity boundary and before
	// MountAll. This harness had no Bridge, which was invisible while every route
	// was untyped (an untyped handler reads the header itself) and would have made
	// every typed op here answer 403 on a request that carries a valid X-Org-Id.
	// It must precede the leaves: fiber runs middleware in registration order.
	app.Use(cloud.Bridge())
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
	return send(t, app, method, path, org, body, false)
}

// send is do() for a caller who may be a platform SuperAdmin. Admin-ness is a
// property of the CALLER and not of the route, so it is one more header on the
// same request rather than a second harness.
func send(t *testing.T, app *zip.App, method, path, org string, body any, admin bool) result {
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
	if admin {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Body: b}
}

// call runs POST /v1/tools/call — the ONE dispatch door onto the dynamic plane.
func call(t *testing.T, app *zip.App, org, name string, args map[string]any) result {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return do(t, app, http.MethodPost, "/v1/tools/call", org,
		map[string]any{"name": name, "arguments": args})
}

// activated lists the caller's callable tools — GET /v1/tools?activated=true, the
// discovery half the dispatch door is paired with.
func activated(t *testing.T, app *zip.App, org string) []string {
	t.Helper()
	return toolNames(t, do(t, app, http.MethodGet, "/v1/tools?activated=true", org, nil).Body)
}

// TestCallGate403: the dispatch door refuses a caller with no validated principal —
// the tool plane never dispatches an unauthenticated request.
func TestCallGate403(t *testing.T) {
	app := newApp(t, nil)
	r := call(t, app, "", "acme_hello", nil)
	if r.Code != 403 {
		t.Fatalf("tools/call without principal want 403, got %d (%s)", r.Code, r.Body)
	}
}

// TestActivationAndCall: the full activation round-trip. A registered source's tool
// is not callable and not in the activated listing until it is switched on via PUT
// /v1/tools/activation; once activated it is listed and POST /v1/tools/call
// dispatches it; an unactivated sibling is refused 403.
func TestActivationAndCall(t *testing.T) {
	app := newApp(t, nil)
	std.Register(&fakeProvider{src: SourceConnector, tools: []Tool{
		tool("acme_hello", SourceConnector),
		tool("acme_secret", SourceConnector),
	}})

	// Before activation: the activated listing is empty, dispatch is 403.
	if names := activated(t, app, "acme"); len(names) != 0 {
		t.Fatalf("pre-activation activated listing must be empty, got %v", names)
	}
	r := call(t, app, "acme", "acme_hello", nil)
	if r.Code != 403 {
		t.Fatalf("unactivated tools/call want 403, got %d (%s)", r.Code, r.Body)
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

	// The activated listing now shows ONLY the activated tool.
	names := activated(t, app, "acme")
	if len(names) != 1 || names[0] != "acme_hello" {
		t.Fatalf("post-activation activated listing must be [acme_hello], got %v", names)
	}

	// tools/call dispatches the activated tool, and answers with its own output.
	r = call(t, app, "acme", "acme_hello", nil)
	if r.Code != 200 || !bytes.Contains(r.Body, []byte(`"by":"connector"`)) {
		t.Fatalf("activated tools/call must dispatch on connector, got %d (%s)", r.Code, r.Body)
	}

	// The still-unactivated sibling stays 403 (activation is per-tool).
	r = call(t, app, "acme", "acme_secret", nil)
	if r.Code != 403 {
		t.Fatalf("sibling unactivated tools/call want 403, got %d (%s)", r.Code, r.Body)
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

	store, err := OpenMCPServerStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenMCPServerStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Write(context.Background(), MCPServer{
		ID: "m123abc", Org: "acme", Name: "myserver", URL: ts.URL,
		AuthHeader: "Authorization", HasSecret: true,
	}, true); err != nil {
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
func (f fakeKMS) DeleteSecret(_ context.Context, ref string) error {
	for id := range f {
		if ref == authRef("acme", id) {
			delete(f, id)
		}
	}
	return nil
}
func (f fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, io.EOF
}

func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode tool listing: %v (%s)", err, body)
	}
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	return names
}
