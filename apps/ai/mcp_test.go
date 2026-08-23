// Copyright © 2026 Hanzo AI. MIT License.

package ai

// mcp_test.go drives ai's REAL MCP server, never a description of it.
//
// It used to compose the WHOLE fleet here — every manifest row, loaded as a
// remote mount carrying that app's committed plugin/<app>/mcp.json — and assert
// the union. That composition is gone with the artifact: the fleet's MCP server
// is no longer the concatenation of files this package can read, it is what the
// subsystems answer when the host asks them, and the only honest place to test
// that is against subsystems that are RUNNING (fleet/mcp_test.go, which starts
// real children on real sockets and goes red on a short list).
//
// What remains here is what belongs here: ai's own op, on ai's own MCP server, and
// the property that a tool call IS an API call — the same gate, the same words, both
// projections. Every assertion reads a BODY. A tools/list that 200s with an empty
// array is the exact failure this fleet has shipped, and a status code cannot
// tell it from a full one.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/manifest"
)

// door is the framework's default MCP path. This file asserts what is BEHIND the
// path, never where it is: the public address is one value the composition root
// owns, and pinning it here would be a second place for it to be written down.
const door = "/mcp"

// served is ai's own op, mounted on its own MCP server, with the identity boundary's
// carrier installed exactly as cloud.Listen installs it.
func served(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "ai", Logger: luxlog.New("aimcptest"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	mountMCP(app)
	return app
}

// rpc posts one JSON-RPC message to the MCP server, optionally as a validated caller
// (the headers SanitizeIdentity mints), and returns the body.
func rpc(t *testing.T, app *zip.App, msg, user, org string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, door, strings.NewReader(msg))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", door, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s answered %d — the MCP server must answer JSON-RPC, body: %s",
			door, resp.StatusCode, trunc(string(b)))
	}
	return string(b)
}

// list asks the MCP server for its tools and returns their names, in the order
// the MCP server served them.
func list(t *testing.T, app *zip.App) []string {
	t.Helper()
	body := rpc(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "", "")
	var env struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("tools/list is not an MCP envelope: %v\nbody: %s", err, trunc(body))
	}
	out := make([]string, 0, len(env.Result.Tools))
	for _, tl := range env.Result.Tools {
		// An op present with an EMPTY description is a SILENT failure: the model
		// pays context for a nameless tool it cannot choose. That exact bug shipped
		// once here (zipdoc blind to group prefixes), so it is a gate, not a hope.
		if strings.TrimSpace(tl.Description) == "" {
			t.Errorf("tool %q has an EMPTY description — the prose zipdoc lifts IS what a model "+
				"reads to pick it. Write the doc comment and run: go generate -run zipdoc ./apps/ai/...", tl.Name)
		}
		if tl.Name == "" || len(tl.InputSchema) == 0 {
			t.Errorf("a tool arrived with no name or no inputSchema: %+v", tl)
		}
		out = append(out, tl.Name)
	}
	return out
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// get drives the REST projection of the same op.
func get(t *testing.T, app *zip.App, user string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ai/mcp/tools", nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-Org-Id", "acme")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// call runs one tools/call and returns (text content, isError).
func call(t *testing.T, app *zip.App, name, user string) (string, bool) {
	t.Helper()
	body := rpc(t, app,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name),
		user, "acme")
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("tools/call is not an MCP envelope: %v\nbody: %s", err, trunc(body))
	}
	if env.Error != nil {
		t.Fatalf("the MCP server refused to dispatch %q: %s", name, env.Error.Message)
	}
	if len(env.Result.Content) == 0 {
		t.Fatalf("tools/call %q returned no content: %s", name, trunc(body))
	}
	return env.Result.Content[0].Text, env.Result.IsError
}

// TestAToolCarriesItsOpsGateExactly: a tool call IS an API call.
//
// Identity is PROPAGATED, never minted — so a caller that cannot reach the op
// over HTTP cannot reach it by naming it as a tool, and the refusal is the SAME
// refusal in the same words. Asserted in both directions, because a gate that
// refuses everyone is not a gate either.
func TestAToolCarriesItsOpsGateExactly(t *testing.T) {
	app := served(t)

	// Anonymous, over HTTP: the op's own 403.
	code, body := get(t, app, "")
	if code != http.StatusForbidden {
		t.Fatalf("anonymous GET answered %d, want 403 — body: %s", code, trunc(body))
	}
	if !strings.Contains(body, mcpGate) {
		t.Fatalf("anonymous GET refused with %q, want the op's own reason %q", trunc(body), mcpGate)
	}

	// Anonymous, over MCP: the SAME refusal, as isError content (the spec's shape
	// — the model reads the reason and reacts), not a transport error.
	text, isErr := call(t, app, "aiMCPTools", "")
	if !isErr {
		t.Fatalf("an anonymous tools/call SUCCEEDED — the tool reached an op the caller "+
			"could not reach over HTTP. It answered: %s", trunc(text))
	}
	if text != mcpGate {
		t.Fatalf("MCP refused with %q; the HTTP route refuses with %q — one op, one reason", text, mcpGate)
	}

	// Validated, over MCP: the REAL body of the REAL op, not an acknowledgement.
	text, isErr = call(t, app, "aiMCPTools", "u-7")
	if isErr {
		t.Fatalf("a validated tools/call was refused: %s", text)
	}
	var got aiMCPSurface
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("the tool result is not the op's Out: %v\ntext: %s", err, trunc(text))
	}
	if want := surface(app, false); got.Tools != want.Tools || len(got.Apps) != len(want.Apps) {
		t.Fatalf("the tool answered {tools:%d apps:%d}; the op answers {tools:%d apps:%d}",
			got.Tools, len(got.Apps), want.Tools, len(want.Apps))
	}
	if len(got.Apps) != len(manifest.Apps) {
		t.Fatalf("the inventory names %d subsystems; the manifest holds %d", len(got.Apps), len(manifest.Apps))
	}
	// This process registered ai's op and nothing else, so its MCP server serves
	// exactly what it declared — and says so.
	if got.Tools != 1 {
		t.Errorf("this process registered 1 typed op; it reports tools=%d", got.Tools)
	}

	// And the same op over HTTP, validated, answers the same body — one op, two
	// projections, never two answers.
	code, body = get(t, app, "u-7")
	if code != http.StatusOK {
		t.Fatalf("validated GET answered %d: %s", code, trunc(body))
	}
	var rest aiMCPSurface
	if err := json.Unmarshal([]byte(body), &rest); err != nil {
		t.Fatalf("the REST body is not the op's Out: %v", err)
	}
	if rest.Tools != got.Tools || len(rest.Apps) != len(got.Apps) {
		t.Errorf("REST answered {tools:%d apps:%d}, MCP answered {tools:%d apps:%d}",
			rest.Tools, len(rest.Apps), got.Tools, len(got.Apps))
	}
}

// TestTheInventoryReadsTheLiveRegistry: the anti-green-surface gate, at the only
// scope a subsystem can honestly answer for.
//
// The op must report what THIS PROCESS's MCP server actually carries, so
// registering a second typed op has to move the number. If it does not, the op is
// reading something other than the registry — which is precisely the instrument this
// fleet keeps mistaking for the mechanism, and the shape of the artifact that was
// just deleted.
func TestTheInventoryReadsTheLiveRegistry(t *testing.T) {
	app := served(t)
	one := surface(app, true)
	if got := len(list(t, app)); one.Tools != got {
		t.Fatalf("the inventory says %d tools; the MCP server serves %d", one.Tools, got)
	}
	if len(one.Names) != one.Tools {
		t.Fatalf("names=%v does not match tools=%d", one.Names, one.Tools)
	}
	if len(one.Names) == 0 || one.Names[0] != "aiMCPTools" {
		t.Fatalf("names=%v, want the op this process registered", one.Names)
	}

	// A SECOND op on the same app: the number moves, or nothing is being read.
	type probeIn struct {
		X string `json:"x"`
	}
	zip.Get(app, "/v1/ai/mcp/probe", func(context.Context, *probeIn) (*probeIn, error) { return nil, nil },
		zip.WithOperationID("aiMCPProbe"), zip.WithSummary("a second op, to prove the count is read and not remembered"))
	two := surface(app, true)
	if two.Tools != one.Tools+1 {
		t.Fatalf("registering an op moved the inventory from %d to %d — it is not reading the live registry",
			one.Tools, two.Tools)
	}
}

// TestAiIsOnItsOwnDoor: the op is a TOOL, because a typed op is one — nothing
// here registers it as such, which is the point.
func TestAiIsOnItsOwnDoor(t *testing.T) {
	names := list(t, served(t))
	if len(names) != 1 || names[0] != "aiMCPTools" {
		t.Fatalf("ai's MCP server carries %v, want exactly [aiMCPTools]", names)
	}
	// The namespace scheme: a hand-written operationId carries its subsystem, so
	// it cannot meet another subsystem's.
	if !strings.HasPrefix(names[0], "ai") {
		t.Errorf("%q does not carry its subsystem — a hand-written id escapes the "+
			"path-derived namespace and must name its owner", names[0])
	}
}
