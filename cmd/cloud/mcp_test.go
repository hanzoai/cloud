package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/webui"
	"github.com/zap-proto/zip"
)

// doMethod is do() for a method other than GET. The MCP defect only shows on
// POST, and the whole point of 308 over 302 is that the method survives, so a
// GET-only helper cannot see either.
func doMethod(t *testing.T, app *zip.App, method, path, body string) (int, string, string, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://cloud"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Location"), string(b)
}

// app308 builds the host the way run() does: the fleet's own MCP endpoint at
// manifest.MCPPath, then the console catch-all LAST. There is no second
// registration in between, and that is the point — webui's handler is TERMINAL,
// so it answers the bare /mcp precisely because nothing else claimed it. This
// test is therefore a test of the COMPOSED host and not of a helper: neuter
// mcpDoor (webui/mcp.go) and every case below fails with the console shell.
//
// The MCP server is the HOST's, registered by fleet.Mount, and zip's is disabled — the
// same pair run() sets. It fronts no app here (an empty composed set), which is
// exactly right for this file: what is being tested is that the ADDRESS is
// reachable and answers JSON-RPC, not what is behind it. What is behind it is
// tested against running subsystems in fleet/mcp_test.go.
func app308(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{
		AppName:               "cloud",
		DisableStartupMessage: true,
		MCP:                   zip.MCPConfig{Disabled: true},
	})
	fleet.Mount(app, manifest.MCPPath, nil, func(string) (string, string, error) {
		return "", "", errNoFleetHere
	})
	if err := webui.Mount(app, consoleBundle()); err != nil {
		t.Fatalf("mount console: %v", err)
	}
	return app
}

// errNoFleetHere: this host composes no subsystem, so nothing is reachable. An
// endpoint that answers anyway is the property under test.
var errNoFleetHere = errors.New("this host composes no subsystems")

// TestBareMCPBeatsTheConsoleCatchAll is the defect, as a test.
//
// Measured on api.hanzo.ai before the fix: POST /mcp = 405 "method not allowed",
// GET /mcp = 200 text/html (the console shell, ~700+ bytes of Next.js). An MCP
// client configured with the host and the conventional /mcp path could not reach
// the MCP server, and neither answer looked like an outage.
//
// Without mcpDoor both probes fall through to webui's SPA fallback and this fails.
func TestBareMCPBeatsTheConsoleCatchAll(t *testing.T) {
	app := app308(t)

	t.Run("POST is not 405 and keeps its method", func(t *testing.T) {
		code, ctype, loc, body := doMethod(t, app, "POST", "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
		if code == http.StatusMethodNotAllowed {
			t.Fatalf("POST /mcp = 405 — the console's GET-only route still owns the path")
		}
		if strings.Contains(ctype, "text/html") {
			t.Fatalf("POST /mcp Content-Type = %q (%d bytes) — the caller got the SPA shell", ctype, len(body))
		}
		if code != http.StatusPermanentRedirect {
			t.Fatalf("POST /mcp = %d, want 308; 301/302 would drop the JSON-RPC body", code)
		}
		if loc != manifest.MCPPath {
			t.Errorf("POST /mcp Location = %q, want %q — the signpost must name the one endpoint", loc, manifest.MCPPath)
		}
	})

	t.Run("GET is not the console shell", func(t *testing.T) {
		code, ctype, loc, body := doMethod(t, app, "GET", "/mcp", "")
		if code == http.StatusOK && strings.Contains(ctype, "text/html") {
			t.Fatalf("GET /mcp = 200 %s (%d bytes) — the console catch-all answered for the MCP endpoint", ctype, len(body))
		}
		if code != http.StatusPermanentRedirect {
			t.Fatalf("GET /mcp = %d, want 308", code)
		}
		if loc != manifest.MCPPath {
			t.Errorf("GET /mcp Location = %q, want %q", loc, manifest.MCPPath)
		}
	})

	// The redirect has to land on something. A 308 to a path the host does not
	// serve is the same dead end with an extra hop, and that is exactly the shape
	// of the bug being fixed — so assert the target is real, not just named.
	t.Run("the target is actually served", func(t *testing.T) {
		code, _, _, body := doMethod(t, app, "POST", manifest.MCPPath,
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
		if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Fatalf("POST /v1/mcp = %d — the signpost points at an endpoint that is not there: %s", code, body)
		}
		if !strings.Contains(body, `"jsonrpc"`) {
			t.Errorf("POST /v1/mcp body = %q, want a JSON-RPC envelope", body)
		}
	})
}
