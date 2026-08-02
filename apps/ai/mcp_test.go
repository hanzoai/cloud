// Copyright © 2026 Hanzo AI. MIT License.

package ai

// mcp_test.go drives the REAL door, never a description of it.
//
// The fleet's door is composed, not written: the host Loads every subsystem
// with the catalogue that subsystem's own binary projected from its own typed-op
// registry, and zip serves the union at one JSON-RPC endpoint. So the honest way
// to test it is to compose it — every manifest row, its real committed
// catalogue — and then ASK it. A remote mount (Plugin.Addr set) records the
// plugin and installs its catalogue without spawning anything (zip load.go), so
// the composition under test is the production one and the test costs no
// processes.
//
// Every assertion below reads a BODY. A tools/list that 200s with an empty array
// is the exact failure this fleet has shipped, and a status code cannot tell it
// from a full one.

import (
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
	"github.com/hanzoai/cloud/plugin"
)

// door is the framework's default MCP path. This file asserts what is BEHIND the
// door, never where it is: the public address is one value the composition root
// owns, and pinning it here would be a second place for it to be written down.
const door = "/mcp"

// fleet composes a host exactly as cmd/cloud does — every manifest row, its real
// catalogue — minus the subsystems named in `without`, which is how a test
// UNMOUNTS one. Nothing is spawned: every plugin is a remote mount at an address
// no request in this file ever reaches, because none of these tests calls a tool
// that belongs to a child.
func fleet(t *testing.T, without ...string) *zip.App {
	t.Helper()
	skip := map[string]bool{}
	for _, n := range without {
		skip[n] = true
	}
	app := zip.New(zip.Config{AppName: "cloud", Logger: luxlog.New("aimcptest"), DisableStartupMessage: true})
	for i, a := range manifest.Apps {
		// A co-resident app routes no prefix of its own, so there is nothing to
		// mount — the same skip cmd/cloud's mount() makes.
		if a.Coresident || skip[a.Name] {
			continue
		}
		p := zip.Plugin{
			Name:  a.Name,
			Addr:  fmt.Sprintf("127.0.0.1:%d", 1+i), // never dialled: no test here calls a child's tool
			Tools: plugin.Tools(a.Name),
		}
		if err := app.Add(zip.Load(p, a.Prefixes...)); err != nil {
			t.Fatalf("compose %s: %v", a.Name, err)
		}
	}
	app.Prepare()
	return app
}

// list asks the door for its tools and returns their names, in the order the
// door served them.
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
		if tl.Name == "" || len(tl.InputSchema) == 0 {
			t.Errorf("a tool arrived with no name or no inputSchema: %+v", tl)
		}
		out = append(out, tl.Name)
	}
	return out
}

// rpc posts one JSON-RPC message to the door, optionally as a validated caller
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", door, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s answered %d — the door must answer JSON-RPC, body: %s",
			door, resp.StatusCode, trunc(string(b)))
	}
	return string(b)
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// TestTheDoorCarriesEverySubsystemsTools: the aggregation, measured.
//
// The number is the union of every subsystem's committed catalogue, and it is
// asserted EXACTLY — a door that lists nothing, or one that quietly drops an
// app, is a different number. The floor beside it is there because "exactly
// equal to a thing computed the same way" is satisfiable by two zeros.
func TestTheDoorCarriesEverySubsystemsTools(t *testing.T) {
	app := fleet(t)
	got := list(t, app)

	want := 0
	for _, a := range manifest.Apps {
		want += len(published()[a.Name])
	}
	if len(got) != want {
		t.Fatalf("the door served %d tools; the fleet's catalogues publish %d", len(got), want)
	}
	if want < 900 {
		t.Fatalf("the fleet publishes only %d tools — a catalogue is missing or empty; "+
			"regenerate: make -f mk/fleet.mk describe-apps", want)
	}
	// A name is dispatch, so two owners make it unroutable.
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("the door lists %q twice — a tool name is dispatch and cannot have two owners", n)
		}
		seen[n] = true
	}
	// Spot-check that a real subsystem's real op actually arrived, so this
	// cannot pass on a list of the right size made of the wrong things.
	for _, want := range []string{"get_v1_o11y_logs", "get_v1_analytics_top", "createOrganization"} {
		if !seen[want] {
			t.Errorf("the door does not carry %q", want)
		}
	}
	t.Logf("the one door carries %d tools across %d subsystems", len(got), len(manifest.Apps))
}

// TestUnmountingASubsystemLeavesTheDoor: THE MUTATION, as a property.
//
// A registry that silently lists nothing is the defect this estate has shipped
// twice, and the reason it survived is that no test could tell a full door from
// an empty one. This one can: unmount o11y and its tools must be GONE — by count
// and by name — while every other subsystem's stay.
func TestUnmountingASubsystemLeavesTheDoor(t *testing.T) {
	const gone = "o11y"
	full := list(t, fleet(t))
	cut := list(t, fleet(t, gone))

	n := len(published()[gone])
	if n == 0 {
		t.Fatalf("%s publishes no tools, so unmounting it proves nothing — pick a subsystem that does", gone)
	}
	if len(full)-len(cut) != n {
		t.Fatalf("unmounting %s changed the door by %d tools; its catalogue holds %d",
			gone, len(full)-len(cut), n)
	}
	left := map[string]bool{}
	for _, s := range cut {
		left[s] = true
	}
	for _, name := range published()[gone] {
		if left[name] {
			t.Errorf("%s is unmounted but the door still lists its tool %q", gone, name)
		}
	}
	// And the rest of the fleet is untouched: an unmount must not take a sibling
	// with it.
	for _, name := range published()["analytics"] {
		if !left[name] {
			t.Errorf("unmounting %s also lost analytics' tool %q", gone, name)
		}
	}
}

// TestTheInventoryAgreesWithTheDoor: the anti-green-surface gate.
//
// ai's op reports what the door carries. If it can report a number the door does
// not serve, it is exactly the instrument this fleet keeps mistaking for the
// mechanism. So it is measured AGAINST the door, on the same app, twice — whole,
// and with a subsystem unmounted.
func TestTheInventoryAgreesWithTheDoor(t *testing.T) {
	for _, without := range [][]string{nil, {"o11y"}, {"o11y", "iam", "admin"}} {
		app := fleet(t, without...)
		if got, want := surface(app, "").Served, len(list(t, app)); got != want {
			t.Errorf("without %v: the inventory says %d tools are served; the door serves %d",
				without, got, want)
		}
	}
	// Published is a property of the BUILD, so unmounting cannot move it — that
	// is the whole reason the two numbers are two fields.
	whole, cut := surface(fleet(t), ""), surface(fleet(t, "o11y"), "")
	if whole.Published != cut.Published {
		t.Errorf("unmounting a subsystem moved `published` (%d → %d) — it reports what the "+
			"BUILD can serve, and only `served` reports what this process did",
			whole.Published, cut.Published)
	}
	if whole.Served == cut.Served {
		t.Errorf("unmounting a subsystem did NOT move `served` (%d) — then it is not reading "+
			"the live composition", whole.Served)
	}
	// The row for an unmounted subsystem says so, and still reports what it would
	// have contributed.
	for _, row := range cut.Apps {
		if row.Name != "o11y" {
			continue
		}
		if row.Served {
			t.Error("o11y is unmounted and its row says served")
		}
		if row.Tools != len(published()["o11y"]) {
			t.Errorf("o11y's row publishes %d tools; its catalogue holds %d", row.Tools, len(published()["o11y"]))
		}
	}
}

// ── the gate ────────────────────────────────────────────────────────────────

// served is ai's own op, mounted on its own door, with the identity boundary's
// carrier installed exactly as cloud.Serve installs it.
func served(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "ai", Logger: luxlog.New("aimcptest"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	mountMCP(app)
	app.Prepare()
	return app
}

// get drives the REST projection of the same op.
func get(t *testing.T, app *zip.App, user string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ai/mcp/tools", nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-Org-Id", "acme")
	}
	resp, err := app.Fiber().Test(req)
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
		t.Fatalf("the door refused to dispatch %q: %s", name, env.Error.Message)
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
	want := surface(app, "")
	if got.Published != want.Published || got.Served != want.Served || got.Local != want.Local {
		t.Fatalf("the tool answered {published:%d served:%d local:%d}; the op answers {published:%d served:%d local:%d}",
			got.Published, got.Served, got.Local, want.Published, want.Served, want.Local)
	}
	if got.Published < 900 || len(got.Apps) != len(manifest.Apps) {
		t.Fatalf("the tool's body is not the fleet's inventory: published=%d over %d rows",
			got.Published, len(got.Apps))
	}
	// This process registered ai's op and nothing else, so its door serves
	// exactly what it declared — and says so.
	if got.Local != 1 || got.Served != 1 {
		t.Errorf("this process registered 1 typed op; it reports local=%d served=%d", got.Local, got.Served)
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
	if rest.Published != got.Published || rest.Served != got.Served {
		t.Errorf("REST answered {published:%d served:%d}, MCP answered {published:%d served:%d}",
			rest.Published, rest.Served, got.Published, got.Served)
	}
}

// TestAiIsOnItsOwnDoor: the op is a TOOL, because a typed op is one — nothing
// here registers it as such, which is the point.
func TestAiIsOnItsOwnDoor(t *testing.T) {
	names := list(t, served(t))
	if len(names) != 1 || names[0] != "aiMCPTools" {
		t.Fatalf("ai's door carries %v, want exactly [aiMCPTools]", names)
	}
	// The namespace scheme: a hand-written operationId carries its subsystem, so
	// it cannot meet another subsystem's.
	if !strings.HasPrefix(names[0], "ai") {
		t.Errorf("%q does not carry its subsystem — a hand-written id escapes the "+
			"path-derived namespace and must name its owner", names[0])
	}
}

// TestOneNameOneOwner: the namespace holds across the whole fleet.
//
// zip refuses a Load whose catalogue claims a name another plugin already owns —
// a BOOT failure. fleet() performs that composition for real, over every
// manifest row, so a collision fails this file before any assertion runs. This
// test states the invariant the composition proves, and names the count so a
// silently emptied catalogue cannot satisfy it.
func TestOneNameOneOwner(t *testing.T) {
	owner := map[string]string{}
	for _, a := range manifest.Apps {
		for _, name := range published()[a.Name] {
			if held, dup := owner[name]; dup {
				t.Errorf("tool %q is claimed by both %q and %q", name, held, a.Name)
			}
			owner[name] = a.Name
		}
	}
	if len(owner) < 900 {
		t.Fatalf("only %d distinct tool names across the fleet", len(owner))
	}
	t.Logf("%d distinct tool names, one owner each", len(owner))
}
