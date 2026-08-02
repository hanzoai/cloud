package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Location"), string(b)
}

// app308 builds the host the way run() does: the MCP door at /v1/mcp, the alias
// in front of it, then the console catch-all LAST. The order is the test — an
// alias registered after webui.Mount would never be reached.
//
// The typed op is not decoration. zip installs the door only when the app has at
// least one op, plugin tool or caller (installMCP), so a host with an empty
// registry serves no /v1/mcp at all — and an alias onto a door that was never
// installed is the very bug this file is about. One op is the smallest thing that
// makes the target real.
func app308(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{
		AppName:               "cloud",
		DisableStartupMessage: true,
		MCP:                   zip.MCPConfig{Path: "/v1/mcp"},
	})
	type ping struct{ Ok bool }
	zip.Get(app, "/v1/ping", func(context.Context, *ping) (*ping, error) {
		return &ping{Ok: true}, nil
	})
	mcpAlias(app)
	if err := webui.Mount(app); err != nil {
		t.Skipf("console embed unavailable in this build: %v", err)
	}
	app.Prepare()
	return app
}

// TestBareMCPBeatsTheConsoleCatchAll is the defect, as a test.
//
// Measured on api.hanzo.ai before the fix: POST /mcp = 405 "method not allowed",
// GET /mcp = 200 text/html (the console shell, ~700+ bytes of Next.js). An MCP
// client configured with the host and the conventional /mcp path could not open
// the door, and neither answer looked like an outage.
//
// Without mcpAlias both probes fall to webui's catch-all and this fails.
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
		if loc != "/v1/mcp" {
			t.Errorf("POST /mcp Location = %q, want %q — the alias must name the one door", loc, "/v1/mcp")
		}
	})

	t.Run("GET is not the console shell", func(t *testing.T) {
		code, ctype, loc, body := doMethod(t, app, "GET", "/mcp", "")
		if code == http.StatusOK && strings.Contains(ctype, "text/html") {
			t.Fatalf("GET /mcp = 200 %s (%d bytes) — the console catch-all answered for the MCP door", ctype, len(body))
		}
		if code != http.StatusPermanentRedirect {
			t.Fatalf("GET /mcp = %d, want 308", code)
		}
		if loc != "/v1/mcp" {
			t.Errorf("GET /mcp Location = %q, want %q", loc, "/v1/mcp")
		}
	})

	// The redirect has to land on something. A 308 to a path the host does not
	// serve is the same dead end with an extra hop, and that is exactly the shape
	// of the bug being fixed — so assert the target is real, not just named.
	t.Run("the target is actually served", func(t *testing.T) {
		code, _, _, body := doMethod(t, app, "POST", "/v1/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
		if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Fatalf("POST /v1/mcp = %d — the alias points at a door that is not there: %s", code, body)
		}
		if !strings.Contains(body, `"jsonrpc"`) {
			t.Errorf("POST /v1/mcp body = %q, want a JSON-RPC envelope", body)
		}
	})
}
