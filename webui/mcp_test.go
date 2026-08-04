package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	zip "github.com/zap-proto/zip"
)

// These tests pin the thing a status code cannot see: WHICH BYTES come back.
// api.hanzo.ai answered GET /mcp with 200 text/html — the console SPA, served by
// the terminal catch-all because the host had moved zip's door to
// manifest.MCPPath and nothing claimed the framework default. A client checking
// for 200 called that healthy. So every assertion below reads the body and the
// content type, and none of them is satisfied by a status alone.

type pingIn struct{}
type pingOut struct {
	OK bool `json:"ok"`
}

// hostApp is the HOST as cmd/cloud builds it: a typed op (so zip has a registry
// to project), the MCP door moved to manifest.MCPPath, and the console mounted
// LAST as the terminal handler — then Prepare(), which is when zip installs the
// door. That order is the one production runs, and it is the order under
// suspicion: the catch-all is registered BEFORE the door exists.
func hostApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: manifest.MCPPath}})
	zip.Get[pingIn, pingOut](app, "/v1/probe/ping",
		func(ctx context.Context, in *pingIn) (*pingOut, error) { return &pingOut{OK: true}, nil })
	if err := Mount(app); err != nil {
		t.Fatal(err)
	}
	return app
}

// pluginApp is a plugin as cloud.Serve builds it: zip's DEFAULT door, which is
// also where a host forwards a composed tools/call, so it must keep answering
// MCP and must not be swallowed by the signpost the host needs.
func pluginApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "plug", DisableStartupMessage: true})
	zip.Get[pingIn, pingOut](app, "/v1/probe/ping",
		func(ctx context.Context, in *pingIn) (*pingOut, error) { return &pingOut{OK: true}, nil })
	if err := Mount(app); err != nil {
		t.Fatal(err)
	}
	return app
}

type reply struct {
	status   int
	ctype    string
	location string
	allow    string
	body     string
}

func call(t *testing.T, app *zip.App, method, path, body string) reply {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Fiber's in-memory Test does not follow redirects, so a 308 is OBSERVED here
	// rather than silently resolved — the hop itself is what this file is about.
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Location"),
		resp.Header.Get("Allow"), string(b)}
}

const toolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

// mcpResult unmarshals an MCP JSON-RPC reply and fails unless it carries a real
// result — not a status, not a shape: the protocol answer itself.
func mcpResult(t *testing.T, got reply, path string) map[string]any {
	t.Helper()
	if got.status != 200 {
		t.Fatalf("%s: status %d, want 200 — body %.120s", path, got.status, got.body)
	}
	if !strings.HasPrefix(got.ctype, "application/json") {
		t.Fatalf("%s: content-type %q, want application/json — body %.120s", path, got.ctype, got.body)
	}
	if strings.Contains(got.body, "<!DOCTYPE") || strings.Contains(got.body, "<!doctype") {
		t.Fatalf("%s: answered with the console SPA shell", path)
	}
	var env struct {
		JSONRPC string         `json:"jsonrpc"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(got.body), &env); err != nil {
		t.Fatalf("%s: body is not JSON-RPC: %v — %.120s", path, err, got.body)
	}
	if env.JSONRPC != "2.0" || env.Result == nil {
		t.Fatalf("%s: not an MCP result — %.200s", path, got.body)
	}
	return env.Result
}

// The money proof: the canonical door answers the MCP protocol, in JSON, with a
// tool list that actually contains the app's typed op.
func TestMCPDoorAnswersMCP(t *testing.T) {
	got := call(t, hostApp(t), http.MethodPost, manifest.MCPPath, toolsList)
	res := mcpResult(t, got, manifest.MCPPath)
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("%s: tools/list returned no tools — %.200s", manifest.MCPPath, got.body)
	}
	t.Logf("POST %s -> %d %s %.140s", manifest.MCPPath, got.status, got.ctype, got.body)
}

// The bug, pinned from both sides: the framework default must never render the
// console, and must send the caller to the door that exists — with the method
// and body intact, which is what 308 (not 302) buys.
func TestFrameworkPathSignpostsTheDoor(t *testing.T) {
	app := hostApp(t)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
		got := call(t, app, m, manifest.FrameworkMCPPath, toolsList)
		if got.status != http.StatusPermanentRedirect {
			t.Errorf("%s %s: status %d, want 308 — %.120s", m, manifest.FrameworkMCPPath, got.status, got.body)
		}
		if got.location != manifest.MCPPath {
			t.Errorf("%s %s: Location %q, want %q", m, manifest.FrameworkMCPPath, got.location, manifest.MCPPath)
		}
		if strings.Contains(got.ctype, "text/html") {
			t.Errorf("%s %s: content-type %q — the machine door must never be HTML", m, manifest.FrameworkMCPPath, got.ctype)
		}
		if strings.Contains(got.body, "<!doctype") || strings.Contains(got.body, "<!DOCTYPE") {
			t.Errorf("%s %s: answered with the console SPA shell", m, manifest.FrameworkMCPPath)
		}
	}
	// And following the hop lands on the real door, speaking MCP.
	got := call(t, app, http.MethodPost, manifest.MCPPath, toolsList)
	mcpResult(t, got, manifest.MCPPath)
}

// A GET on the door is a client asking for the optional SSE stream. There is
// none, so MCP Streamable HTTP wants 405 + Allow — not the 404 that says the
// door is absent.
func TestDoorRefusesNonPOSTHonestly(t *testing.T) {
	got := call(t, hostApp(t), http.MethodGet, manifest.MCPPath, "")
	if got.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s: status %d, want 405 — %.120s", manifest.MCPPath, got.status, got.body)
	}
	if got.allow != http.MethodPost {
		t.Errorf("GET %s: Allow %q, want POST", manifest.MCPPath, got.allow)
	}
	if strings.Contains(got.ctype, "text/html") {
		t.Errorf("GET %s: content-type %q — never HTML on the door", manifest.MCPPath, got.ctype)
	}
}

// The signpost is SELF-SCOPING: it is in the terminal handler, so it only fires
// for a path no route claimed. A plugin serving its own door at the framework
// default — which is where a host forwards a composed tools/call — must still
// answer MCP there, not redirect to an address it does not serve.
func TestPluginKeepsItsOwnDoor(t *testing.T) {
	app := pluginApp(t)
	got := call(t, app, http.MethodPost, manifest.FrameworkMCPPath, toolsList)
	mcpResult(t, got, manifest.FrameworkMCPPath)
	t.Logf("plugin POST %s -> %d %s %.100s", manifest.FrameworkMCPPath, got.status, got.ctype, got.body)

	// On a plugin the host's address is the unclaimed one, and a POST there is a
	// genuine miss — the ordinary /v1/ 404, never HTML, never a false 405.
	miss := call(t, app, http.MethodPost, manifest.MCPPath, toolsList)
	if miss.status != http.StatusNotFound {
		t.Errorf("plugin POST %s: status %d, want 404 — %.120s", manifest.MCPPath, miss.status, miss.body)
	}
}

// The other half of the contract, and the one a fix must not break: the console
// still serves, at "/" and at a client-side deep link.
func TestConsoleStillServes(t *testing.T) {
	app := hostApp(t)
	for _, p := range []string{"/", "/settings", "/mcp-servers"} {
		got := call(t, app, http.MethodGet, p, "")
		if got.status != 200 {
			t.Errorf("GET %s: status %d, want 200", p, got.status)
		}
		if !strings.Contains(got.ctype, "text/html") {
			t.Errorf("GET %s: content-type %q, want text/html", p, got.ctype)
		}
		if !strings.Contains(strings.ToLower(got.body), "<!doctype html") {
			t.Errorf("GET %s: not the console shell — %.120s", p, got.body)
		}
	}
	// A path merely PREFIXED by the framework door is an ordinary console route
	// and must keep rendering: the rule matches the address, not a subtree.
	if got := call(t, app, http.MethodGet, "/mcp-servers", ""); !strings.Contains(got.ctype, "text/html") {
		t.Errorf("/mcp-servers: content-type %q — the signpost swallowed a console route", got.ctype)
	}
}
