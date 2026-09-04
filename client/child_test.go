// Copyright © 2026 Hanzo AI. MIT License.

package client_test

// A subsystem with NO typed op is still ASKED, so it must still ANSWER.
//
// The rest of this package's tests build children out of typed ops, which is the
// half of the surface that was never broken. Thirty of the surface's 117 subsystems
// have no typed op at all — their routes are raw handlers, or they belong to
// another module — and for those zip returned before registering an MCP route
// (zip@v1.25.1 mcp.go:99: no registry, no plugin catalogue, no per-caller Source
// ⇒ no route). Nothing claimed POST /mcp in those processes, the ask fell through
// to the console's terminal handler, and it answered the signpost that is right
// only on the public endpoint: 308 → /v1/mcp, which inside a child is a 404. The
// MCP server read the non-2xx as an outage (client/surface.go, ask) and reported
// thirty healthy subsystems as unreachable:
//
//	"exec answered 308 for /mcp"
//
// So this test composes a REAL one of those thirty the way cloud.Serve composes a
// plugin child — cloud.App, its own Mount, the console mounted LAST as the
// terminal handler — puts it on a unix socket, and drives the composed MCP
// server over that wire. apps/exec is the representative: 56 operations, every
// one of them a raw reverse-proxy route by design (apps/exec/typed_wire_test.go
// is that ledger), so its registry is empty for the same reason the other
// twenty-nine are.
//
// It asserts the ANSWER, never a status: the child's own reply must be a JSON-RPC
// result carrying a tools array, and the surface's MCP server must name no outage
// for it. An empty array is a real answer — "asked, serves nothing" is a
// different fact from "could not be asked", and telling those apart is what
// package surface is for.

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/exec"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sock"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/webui"
)

// darkChild brings up apps/exec as its own plugin process does, on its own
// socket. Nothing here is a fixture: cloud.App is the one constructor every
// plugin main reaches Serve through, UseAll is the loop Serve runs, and
// webui.Use is the terminal handler Serve installs last in EVERY process —
// which is the handler that answered the ask.
func darkChild(t *testing.T, name string, mount cloud.UseFunc) *child {
	t.Helper()
	cfg := &cloud.Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: t.TempDir(), Enable: []string{name}}
	deps := cloud.BuildDeps(cfg)

	app := cloud.App(name, cfg, deps, nil)
	if err := cloud.UseAll(app, []cloud.Plugin{{Name: name, Price: cloud.Free, Use: mount}}, cfg, deps); err != nil {
		t.Fatalf("mount %s: %v", name, err)
	}
	// LAST, and with no console bundle — a child cannot bootstrap one (serve.go),
	// so this is the shape production runs.
	if err := webui.Use(app, nil); err != nil {
		t.Fatalf("console %s: %v", name, err)
	}

	sock := filepath.Join(sock.Dir(t), name+".sock")
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, sock)
	return &child{name: name, addr: sock, app: app}
}

// TestASubsystemWithNoTypedOpStillAnswersTheEndpoint is the regression, from both
// ends of the hop.
func TestASubsystemWithNoTypedOpStillAnswersTheEndpoint(t *testing.T) {
	kid := darkChild(t, "exec", exec.Use)

	// END ONE — the child's own MCP server, at the address the surface asks. The claim is
	// about the BYTES: a JSON-RPC result with a tools array. A 308 has neither, and
	// so did every one of the thirty.
	req, err := http.NewRequest(http.MethodPost, manifest.FrameworkMCPPath, strings.NewReader(toolsListBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := kid.app.Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", manifest.FrameworkMCPPath, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("the child SIGNPOSTED its own MCP address to %q — that address is a 404 in this process, "+
			"which is why following the hop is not the fix", loc)
	}
	var env struct {
		JSONRPC string `json:"jsonrpc"`
		Result  *struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.JSONRPC != "2.0" || env.Result == nil {
		t.Fatalf("POST %s did not answer MCP: status %d — %.160s",
			manifest.FrameworkMCPPath, resp.StatusCode, body)
	}
	if env.Result.Tools == nil {
		t.Errorf("tools/list answered without a tools array — %.160s", body)
	}
	t.Logf("exec POST %s -> %d, %d tools", manifest.FrameworkMCPPath, resp.StatusCode, len(env.Result.Tools))

	// END TWO — the composed MCP server over the same child, over its real socket. A
	// subsystem that answers must not be NAMED as an outage: that list is what a
	// client reads to know its catalogue is short, so a false entry there is the
	// same lie as a silently short list.
	h := host(t, []string{"exec"}, map[string]*child{"exec": kid})
	res := rpc(t, h, toolsListBody)
	for _, o := range outages(t, res) {
		if o.App == "exec" {
			t.Fatalf("the MCP server reports exec unreachable: %q — it is up and it answered", o.Error)
		}
	}
	if _, ok := res["tools"]; !ok {
		t.Fatalf("the MCP server answered without a tools array — %v", res)
	}
}

// outages reads the MCP server's own outage list off the result's _meta.
func outages(t *testing.T, res map[string]any) []client.Outage {
	t.Helper()
	meta, _ := res["_meta"].(map[string]any)
	if meta == nil {
		return nil
	}
	raw, err := json.Marshal(meta[client.Unavailable])
	if err != nil {
		t.Fatal(err)
	}
	var out []client.Outage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not a list of outages: %v — %s", client.Unavailable, err, raw)
	}
	return out
}

const toolsListBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
