package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// door_test.go pins the seam that makes the catalog worth having: a server an org
// ENABLED shows up as tools on the fleet's one MCP door, namespaced by the server
// it came from, callable, and invisible to every other tenant.

// doorApp mounts the tool plane on an app whose MCP door has the per-caller half
// wired — the same composition plugin/tools/main.go declares. Prepare() is what
// installs the door, so it is called here rather than left to Listen.
func doorApp(t *testing.T) *zip.App {
	t.Helper()
	old := std
	std = NewRegistry()
	t.Cleanup(func() { std = old })

	app := zip.New(zip.Config{Logger: luxlog.New("test"), MCP: zip.MCPConfig{Source: Door()}})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// rpc posts one JSON-RPC message to the door as org.
func rpc(t *testing.T, app *zip.App, org, body string) map[string]any {
	t.Helper()
	r := send(t, app, http.MethodPost, "/mcp", org, json.RawMessage(body), false)
	if r.Code != http.StatusOK {
		t.Fatalf("mcp %s: %d (%s)", body, r.Code, r.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("mcp response: %v (%s)", err, r.Body)
	}
	return out
}

// doorTools is the tool names the door lists for org.
func doorTools(t *testing.T, app *zip.App, org string) map[string]bool {
	t.Helper()
	res, _ := rpc(t, app, org, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)["result"].(map[string]any)
	list, _ := res["tools"].([]any)
	out := map[string]bool{}
	for _, raw := range list {
		if m, ok := raw.(map[string]any); ok {
			name, _ := m["name"].(string)
			out[name] = true
		}
	}
	return out
}

// remoteServer is a stand-in for a vendor's hosted MCP server: it answers
// tools/list and tools/call, and refuses a request that arrives without the
// credential the org sealed in KMS.
func remoteServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"tools": []map[string]any{{"name": "charge", "description": "take a payment",
					"inputSchema": map[string]any{"type": "object"}}},
			}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"charged": req.Params.Arguments["amount"],
			}})
		default:
			http.Error(w, "bad method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// enable registers one external server for org and puts its provider on the
// registry with a client that can reach the loopback test server (the SSRF-guarded
// dialer is production's, and is tested on its own).
func enable(t *testing.T, org, id, listing, url string) {
	t.Helper()
	if mounted == nil {
		t.Fatal("the tool plane is not mounted")
	}
	if _, err := mounted.State.servers.Write(context.Background(), MCPServer{
		ID: id, Org: org, Name: "vendor", URL: url, AuthHeader: "Authorization",
		HasSecret: true, Listing: listing,
	}, true); err != nil {
		t.Fatalf("create server: %v", err)
	}
}

// TestDoorListsAnEnabledServersTools: the whole point. An org enables a catalog
// listing; its tools are on the fleet's ONE door, prefixed by the server they came
// from, and calling one reaches the vendor with the org's own credential.
func TestDoorListsAnEnabledServersTools(t *testing.T) {
	ts := remoteServer(t)
	app := doorApp(t)
	enable(t, "acme", "stripe", "com.stripe_mcp", ts.URL)

	p := newMCPProvider(mounted.State.servers, fakeKMS{"stripe": "Bearer sk-live"})
	p.http = ts.Client()
	std.Register(p)

	// Unactivated, it is not on the door: listing a name that answers 403 would be
	// worse than not listing it.
	if names := doorTools(t, app, "acme"); names["stripe_charge"] {
		t.Fatal("an unactivated tool must not be on the door")
	}

	if r := do(t, app, http.MethodPut, "/v1/tools/activation", "acme",
		map[string]any{"activate": []string{"stripe_charge"}}); r.Code != 200 {
		t.Fatalf("activate: %d (%s)", r.Code, r.Body)
	}

	names := doorTools(t, app, "acme")
	if !names["stripe_charge"] {
		t.Fatalf("the enabled server's tool is not on the door: %v", names)
	}

	// And it RUNS, through the tool plane's one dispatch policy.
	res, _ := rpc(t, app, "acme",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"stripe_charge","arguments":{"amount":42}}}`)["result"].(map[string]any)
	body, _ := json.Marshal(res)
	if strings.Contains(string(body), `"isError":true`) || !strings.Contains(string(body), "42") {
		t.Fatalf("tools/call did not reach the vendor: %s", body)
	}
}

// TestDoorIsNamespacedPerServer: two servers offering the same remote tool name
// stay apart, because the server id prefixes it. Without this an org's second
// vendor would silently shadow its first.
func TestDoorIsNamespacedPerServer(t *testing.T) {
	ts := remoteServer(t)
	app := doorApp(t)
	enable(t, "acme", "stripe", "com.stripe_mcp", ts.URL)
	enable(t, "acme", "adyen", "com.adyen_mcp", ts.URL)

	p := newMCPProvider(mounted.State.servers, fakeKMS{"stripe": "Bearer sk-live", "adyen": "Bearer sk-live"})
	p.http = ts.Client()
	std.Register(p)

	if r := do(t, app, http.MethodPut, "/v1/tools/activation", "acme",
		map[string]any{"activate": []string{"stripe_charge", "adyen_charge"}}); r.Code != 200 {
		t.Fatalf("activate: %d (%s)", r.Code, r.Body)
	}
	names := doorTools(t, app, "acme")
	if !names["stripe_charge"] || !names["adyen_charge"] {
		t.Fatalf("both vendors' charge must be on the door under their own names: %v", names)
	}
}

// TestDoorIsPerTenant: org B never sees org A's tools on the door, and cannot call
// one by naming it. The tenancy comes from the validated principal, so there is no
// field a caller could set to reach across.
func TestDoorIsPerTenant(t *testing.T) {
	ts := remoteServer(t)
	app := doorApp(t)
	enable(t, "acme", "stripe", "com.stripe_mcp", ts.URL)

	p := newMCPProvider(mounted.State.servers, fakeKMS{"stripe": "Bearer sk-live"})
	p.http = ts.Client()
	std.Register(p)
	if r := do(t, app, http.MethodPut, "/v1/tools/activation", "acme",
		map[string]any{"activate": []string{"stripe_charge"}}); r.Code != 200 {
		t.Fatalf("activate: %d (%s)", r.Code, r.Body)
	}

	if names := doorTools(t, app, "rival"); names["stripe_charge"] {
		t.Fatalf("another tenant sees acme's tool on the door: %v", names)
	}
	res, _ := rpc(t, app, "rival",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stripe_charge","arguments":{"amount":42}}}`)["result"].(map[string]any)
	body, _ := json.Marshal(res)
	if !strings.Contains(string(body), `"isError":true`) {
		t.Fatalf("another tenant's tools/call must be refused, got %s", body)
	}
}

// TestServersArePerTenant: the registration surface is scoped the same way, and no
// route ever returns a credential VALUE.
func TestServersArePerTenant(t *testing.T) {
	app := doorApp(t)
	enable(t, "acme", "stripe", "com.stripe_mcp", "https://mcp.stripe.com")

	mine := do(t, app, http.MethodGet, "/v1/mcp/servers", "acme", nil)
	if !strings.Contains(string(mine.Body), `"stripe"`) {
		t.Fatalf("an org must see its own server: %s", mine.Body)
	}
	if strings.Contains(string(mine.Body), "sk-live") || strings.Contains(string(mine.Body), `"secret"`) {
		t.Fatalf("a credential VALUE reached the wire: %s", mine.Body)
	}
	if !strings.Contains(string(mine.Body), `"source":"catalog"`) {
		t.Fatalf("an enabled listing must record where it came from: %s", mine.Body)
	}

	theirs := do(t, app, http.MethodGet, "/v1/mcp/servers", "rival", nil)
	if strings.Contains(string(theirs.Body), "stripe") {
		t.Fatalf("another tenant sees acme's server: %s", theirs.Body)
	}
	// And cannot delete it either: an id belonging to another tenant is a 404.
	if r := do(t, app, http.MethodDelete, "/v1/mcp/servers/stripe", "rival", nil); r.Code != 404 {
		t.Fatalf("cross-tenant delete want 404, got %d (%s)", r.Code, r.Body)
	}
}

// TestAnonymousDoorIsTheFleetsOwn: a tools/list that names no caller gets the
// build-time half and nothing else — the memcpy that makes the door affordable is
// not spent asking about a tenant who is not there.
func TestAnonymousDoorIsTheFleetsOwn(t *testing.T) {
	ts := remoteServer(t)
	app := doorApp(t)
	enable(t, "acme", "stripe", "com.stripe_mcp", ts.URL)
	p := newMCPProvider(mounted.State.servers, fakeKMS{"stripe": "Bearer sk-live"})
	p.http = ts.Client()
	std.Register(p)

	names := doorTools(t, app, "")
	if names["stripe_charge"] {
		t.Fatalf("an anonymous list must carry no tenant's tools: %v", names)
	}
	if !names["post_tools_call"] {
		t.Fatalf("the projected ops must still be listed: %v", names)
	}
}

// TestTheListingCacheIsPerServerAndPerTenant: one tools/list per server per
// window, and never a list one org can read off another's key.
//
// Without the window every dispatch paid it: resolving a tool name lists every
// provider, and this provider asked every one of the org's servers over the
// network — so thirty enabled listings meant thirty outbound requests per tool
// call, each with a 20s timeout, on the path a model drives.
func TestTheListingCacheIsPerServerAndPerTenant(t *testing.T) {
	var lists atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "tools/list" {
			lists.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"tools": []map[string]any{{"name": "charge", "description": "d", "inputSchema": map[string]any{"type": "object"}}},
		}})
	}))
	defer ts.Close()

	store, err := OpenMCPServerStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenMCPServerStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	for _, org := range []string{"acme", "rival"} {
		if _, err := store.Write(ctx, MCPServer{ID: "v", Org: org, Name: "v", URL: ts.URL}, true); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	p := newMCPProvider(store, nil)
	p.http = ts.Client()

	for range 5 {
		if tools, err := p.List(ctx, Scope{Org: "acme"}); err != nil || len(tools) != 1 {
			t.Fatalf("List: %v %v", tools, err)
		}
	}
	if n := lists.Load(); n != 1 {
		t.Fatalf("five listings asked the server %d times, want 1", n)
	}
	// A DIFFERENT tenant with the same server id and URL must ask for itself: the
	// key carries the org, so nothing is shared across the boundary.
	if _, err := p.List(ctx, Scope{Org: "rival"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if n := lists.Load(); n != 2 {
		t.Fatalf("a second tenant read the first's cached list (asked %d times, want 2)", n)
	}
}
