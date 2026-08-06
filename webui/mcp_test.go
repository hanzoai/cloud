package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	zapmcp "github.com/zap-proto/mcp"
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

// testDoor is a process's agent door for the tests that drive the console
// handler directly. It is a REAL zip.App's — the same value Mount hands in — so
// no test is exercising a shape production does not have.
func testDoor() zapmcp.Handler {
	return zip.New(zip.Config{AppName: "console", DisableStartupMessage: true}).MCP
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
	if err := Mount(app, testBundle()); err != nil {
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
	if err := Mount(app, testBundle()); err != nil {
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

// The original bug, still pinned: the framework default must never render the
// console. What it is answered WITH has changed — this process's own door, not a
// redirect. The 308 belongs to a host that moved its door and is registered by
// the same call that registers the target (fleet.Mount); the end-to-end host is
// pinned in cmd/cloud/mcp_test.go, where both halves are composed. Here the rule
// is the one the terminal handler can actually keep on its own.
func TestFrameworkPathIsAnsweredAsADoor(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  *zip.App
	}{
		{"door moved off the framework default", hostApp(t)},
		{"no door mounted at all", doorlessApp(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
				got := call(t, tc.app, m, manifest.FrameworkMCPPath, toolsList)
				if strings.Contains(got.ctype, "text/html") {
					t.Errorf("%s %s: content-type %q — the machine door must never be HTML", m, manifest.FrameworkMCPPath, got.ctype)
				}
				if strings.Contains(got.body, "<!doctype") || strings.Contains(got.body, "<!DOCTYPE") {
					t.Errorf("%s %s: answered with the console SPA shell", m, manifest.FrameworkMCPPath)
				}
				// Never a hop to an address this process does not serve. That is the
				// defect: kms sent its own door to /v1/mcp and /v1/mcp 404'd.
				if got.location != "" {
					t.Errorf("%s %s: Location %q — the door is HERE; nothing to redirect to",
						m, manifest.FrameworkMCPPath, got.location)
				}
				if m == http.MethodPost {
					mcpResult(t, got, manifest.FrameworkMCPPath)
					continue
				}
				// The optional SSE stream, which this door does not have.
				if got.status != http.StatusMethodNotAllowed {
					t.Errorf("%s %s: status %d, want 405", m, manifest.FrameworkMCPPath, got.status)
				}
				if got.allow != http.MethodPost {
					t.Errorf("%s %s: Allow %q, want POST", m, manifest.FrameworkMCPPath, got.allow)
				}
			}
		})
	}
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

// doorlessApp is the shape kms actually has, and the one nothing here modelled:
// a plugin whose REST surface is PLAIN handlers and whose four typed ops live on
// the internal plane (cloud.Plane, apps/kms/secret_rpc.go) so that no route runs
// from the edge to a secret. Its own registry is therefore EMPTY — and zip mounts
// the /mcp route only when it has something to expose (zip mcp.go installMCP), so
// this app never gets a door and the console catch-all is what answers at it.
//
// nil bundle on purpose: a child cannot bootstrap the console release, so every
// per-app plugin binary runs exactly this way.
func doorlessApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	app.Get("/v1/kms/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})
	if err := Mount(app, nil); err != nil {
		t.Fatal(err)
	}
	return app
}

// A door with nothing behind it is still a door.
//
// Measured on bin/kms: POST /mcp -> 308 Location /v1/mcp, and POST /v1/mcp -> 404.
// The fleet asks every child at FrameworkMCPPath and reads any non-2xx as an
// outage (fleet/fleet.go ask), so a child that redirects its own door to an
// address it does not serve drops out of the composed tool list AND is reported
// down — for the crime of having no tools. An empty list is the honest answer and
// it is a 200.
func TestDoorlessPluginAnswersItsOwnDoor(t *testing.T) {
	app := doorlessApp(t)

	got := call(t, app, http.MethodPost, manifest.FrameworkMCPPath, toolsList)
	res := mcpResult(t, got, manifest.FrameworkMCPPath)
	if _, ok := res["tools"]; !ok {
		t.Fatalf("POST %s: result has no tools array — %.200s", manifest.FrameworkMCPPath, got.body)
	}
	t.Logf("doorless POST %s -> %d %s %.140s", manifest.FrameworkMCPPath, got.status, got.ctype, got.body)

	// initialize is the handshake every MCP client opens with; a door that only
	// answered tools/list would fail before it ever asked.
	init := call(t, app, http.MethodPost, manifest.FrameworkMCPPath,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if res := mcpResult(t, init, manifest.FrameworkMCPPath); res["protocolVersion"] == nil {
		t.Errorf("initialize: no protocolVersion — %.200s", init.body)
	}

	// ONE door: an agent must not be able to tell whether zip's route or the
	// terminal handler carried the answer. A media type that differed would be the
	// second door reappearing as a header.
	mounted := call(t, pluginApp(t), http.MethodPost, manifest.FrameworkMCPPath, toolsList)
	if got.ctype != mounted.ctype {
		t.Errorf("content-type %q via the terminal handler vs %q via zip's route — one door, one answer",
			got.ctype, mounted.ctype)
	}
}

// Registration order was the suspect and it is innocent — pinned here so the next
// reader does not re-run the investigation.
//
// The console catch-all is registered FIRST (Mount, from cloud.Serve) and zip's
// door is a CONTROL route installed LAST, in prepare() at Listen (zip generation.go
// materialise puts ctl on after every entry). Upstream fiber would let the earlier
// /* win. The zap-proto fork does not: endpoint routes are inserted
// most-specific-first within the run that follows the last middleware barrier
// (fiber router_precedence.go insertRouteSorted), so the static /mcp sorts AHEAD
// of the greedy /* however late it arrives.
func TestCatchAllNeverShadowsTheDoor(t *testing.T) {
	app := pluginApp(t) // catch-all first; the door does not exist yet
	// Test() runs prepare(), which is where installMCP registers the control route.
	mcpResult(t, call(t, app, http.MethodPost, manifest.FrameworkMCPPath, toolsList),
		manifest.FrameworkMCPPath)

	var order []string
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method != http.MethodPost {
			continue
		}
		if r.Path == manifest.FrameworkMCPPath || r.Path == "/*" {
			order = append(order, r.Path)
		}
	}
	if len(order) != 2 || order[0] != manifest.FrameworkMCPPath {
		t.Fatalf("POST stack order %v — the door must sort ahead of the catch-all", order)
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
