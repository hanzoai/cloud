// Copyright © 2026 Hanzo AI. MIT License.

package fleet_test

// The grouped surface, over the wire, at the scale that broke the flat one.
//
// The measurement these tests exist to hold is not a ratio someone computed. It
// was taken from the deployed door:
//
//	$ curl -s https://api.hanzo.ai/v1/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
//	200  977636 bytes   1189 tools   (2026-08-06, _meta: 134 refused, 31 apps unavailable)
//
// ~244,000 tokens to enumerate what can be called, and Slack keeps 128 of them.
// So [liveFlatBytes] below is a real number from a real server, and
// TestTheWholeFleetFitsInAModelsHead puts the SAME fleet through the new door
// and prints what it costs now.
//
// "The same fleet" is literal: the corpus is plugin/*/openapi.json — each
// subsystem's own spec, written by its own binary, which is where its operation
// ids come from in the first place. Names someone invented for a test would
// measure a fleet that does not exist.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// liveFlatBytes and liveFlatTools are the deployed door's flat tools/list, as
// measured. See the file comment.
const (
	liveFlatBytes = 977636
	liveFlatTools = 1189
)

// slackKeeps is the cap that makes this a correctness problem rather than a
// verbosity one: a client that saves the first N tools makes everything after N
// permanently unreachable.
const slackKeeps = 128

// ---------------------------------------------------------------------------
// the fleet's own operation corpus
// ---------------------------------------------------------------------------

// corpus is every operation id this fleet declares, by the subsystem that
// declares it, read from the plugins' own OpenAPI documents.
func corpus(t *testing.T) map[string][]string {
	t.Helper()
	specs, err := filepath.Glob(filepath.Join("..", "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("no plugin specs at ../plugin/*/openapi.json (%v): the corpus is this fleet's own ops, not invented ones", err)
	}
	out := map[string][]string{}
	for _, path := range specs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		app := filepath.Base(filepath.Dir(path))
		seen := map[string]bool{}
		for _, methods := range doc.Paths {
			for _, op := range methods {
				if op.OperationID == "" || seen[op.OperationID] {
					continue
				}
				seen[op.OperationID] = true
				out[app] = append(out[app], op.OperationID)
			}
		}
		sort.Strings(out[app])
	}
	return out
}

// serving brings up one child per app, each declaring the operation ids given,
// and returns a door over all of them.
//
// The routes are this test's, the ids are the fleet's: zip derives a tool name
// from the route only when nobody declared one, and every op here declares.
func serving(t *testing.T, by map[string][]string) *zip.App {
	t.Helper()
	dir := t.TempDir()
	kids := map[string]*child{}
	apps := make([]string, 0, len(by))
	for app := range by {
		apps = append(apps, app)
	}
	sort.Strings(apps) // the manifest's order is arbitrary here; a stable one is not
	for _, app := range apps {
		kids[app] = serve(t, dir, app, by[app])
	}
	return host(t, apps, kids)
}

func serve(t *testing.T, dir, name string, ops []string) *child {
	t.Helper()
	sock := filepath.Join(dir, name+".sock")
	a := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	for i, id := range ops {
		zip.Post(a, "/v1/"+name+"/op"+strconv.Itoa(i), func(_ context.Context, in *thingIn) (*thingOut, error) {
			return &thingOut{App: name, Which: in.Which}, nil
		}, zip.WithOperationID(id), zip.WithSummary("what "+name+" does at "+id))
	}
	go func() { _ = a.Listen(sock) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	waitFor(t, sock)
	return &child{name: name, addr: sock, app: a}
}

// ---------------------------------------------------------------------------
// what the door publishes
// ---------------------------------------------------------------------------

// TestTheDoorPublishesOneToolPerSubsystem is the shape of the answer: a tool per
// app that has something to offer, plus describe. Nothing else.
func TestTheDoorPublishesOneToolPerSubsystem(t *testing.T) {
	h := serving(t, map[string][]string{
		"ai":    {"post_v1_chat_completions", "get_v1_models"},
		"git":   {"post_v1_git_repos", "get_v1_git_repos"},
		"iam":   {"CreateUser", "DeleteUser"}, // every op refused: no tool at all
		"quiet": {},                           // nothing to offer: no tool at all
	})
	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	got := names(res)
	want := []string{fleet.Describe, "ai", "git"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the door publishes %v, want %v", got, want)
	}
	// describe leads because everything else is unusable without it; the
	// subsystems then follow in productStems order, chat before git.
	if got[0] != fleet.Describe {
		t.Errorf("the first tool is %q; the tool a truncated surface cannot do without leads", got[0])
	}
	// Within an enum the order is gather's: rank first (chat before models),
	// then name inside a bucket (both git ops share one).
	if ops := offered(res); strings.Join(ops, ",") !=
		"post_v1_chat_completions,get_v1_models,get_v1_git_repos,post_v1_git_repos" {
		t.Errorf("the enums carry %v; within a subsystem the product surface still leads", ops)
	}
	// Names only. The 977 KB was the schemas, so an enum that carried them would
	// have moved the problem rather than solved it.
	for _, tl := range published(res) {
		if b, _ := json.Marshal(tl); strings.Contains(string(b), `"which"`) {
			t.Errorf("a published tool carries an operation's own schema: %s", b)
		}
	}
}

// TestNoSubsystemIsCalledDescribe is the one thing dropping the `hanzo_` prefix
// put at risk, checked where it is decidable: the door's tools are the app names
// plus [fleet.Describe], so an app called `describe` would publish a SECOND tool
// under that name and the door would answer it as its own — the subsystem
// silently unreachable, with nothing in either file to say why.
//
// It reads the manifest, which is the fleet's source of truth for app names, so
// the collision is caught when the row is added rather than when a model calls it.
func TestNoSubsystemIsCalledDescribe(t *testing.T) {
	for _, a := range manifest.Apps {
		if a.Name == fleet.Describe {
			t.Fatalf("manifest declares an app named %q, which is also the door's own tool; "+
				"rename the app or rename the tool — they cannot share one name", a.Name)
		}
	}
}

// TestTheWholeFleetFitsInAModelsHead is the measurement, over this fleet's own
// operation corpus, through the real door, on the wire.
func TestTheWholeFleetFitsInAModelsHead(t *testing.T) {
	by := corpus(t)
	declared := 0
	for _, ops := range by {
		declared += len(ops)
	}
	h := serving(t, by)

	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	tools, ops := names(res), offered(res)

	// Every published tool is ONE subsystem of the corpus, and every name in its
	// enum is an operation THAT subsystem declared. Nothing leaks between apps,
	// and nothing is invented.
	owns := map[string]map[string]bool{}
	for app, ids := range by {
		owns[app] = map[string]bool{}
		for _, id := range ids {
			owns[app][id] = true
		}
	}
	subsystems := 0
	for _, tl := range published(res) {
		m, _ := tl.(map[string]any)
		name, _ := m["name"].(string)
		if name == fleet.Describe {
			continue
		}
		subsystems++
		app := name
		if owns[app] == nil {
			t.Fatalf("the door published %q and no such subsystem is in the corpus", name)
		}
		for _, op := range offered(map[string]any{"tools": []any{tl}}) {
			if !owns[app][op] {
				t.Fatalf("%s offers %q, which %s does not declare — the grouping is not by owner", name, op, app)
			}
		}
	}
	if len(tools) != subsystems+1 {
		t.Errorf("published %d tools for %d subsystems; the surface is one per subsystem plus %s",
			len(tools), subsystems, fleet.Describe)
	}
	// Reachable, not merely listed: an op in two enums would be ambiguous, an op
	// in none would be lost. This is what makes the change a smaller surface and
	// not a shorter one.
	once := map[string]bool{}
	for _, n := range ops {
		if once[n] {
			t.Fatalf("%q appears in two enums", n)
		}
		once[n] = true
	}

	if len(tools) >= slackKeeps {
		t.Fatalf("the door publishes %d tools and a client keeps %d — the cap is still binding", len(tools), slackKeeps)
	}
	if tools[0] != fleet.Describe {
		t.Errorf("the first tool is %q; a client that truncates must keep the one tool the enums cannot be read without", tools[0])
	}
	// The claim is per OPERATION, because this corpus is the whole fleet and the
	// baseline was taken while 31 subsystems were down: a surface of names must
	// cost an order of magnitude less per op than a surface of schemas.
	was, now := float64(liveFlatBytes)/float64(liveFlatTools), float64(len(body))/float64(len(ops))
	if now > was/10 {
		t.Fatalf("an operation costs %.0f bytes to enumerate, against %.0f flat", now, was)
	}

	t.Logf("MEASURED — the fleet's own corpus (plugin/*/openapi.json), one child per subsystem:")
	// The names themselves, because they are the surface a model reads and a bare
	// list is the only way to SEE that they carry no prefix. The loop above already
	// fails if one does — `app := name` is the whole mapping now — but a reader of
	// this output should not have to take that on faith.
	t.Logf("  the head of the surface  %s …", strings.Join(tools[:min(12, len(tools))], " "))
	t.Logf("  operations declared      %5d across %d subsystems", declared, len(by))
	t.Logf("  operations offered       %5d (%d withheld by refuse())", len(ops), declared-len(ops))
	t.Logf("  BEFORE  flat tools/list  %5d tools  %8d bytes  %6.0f B/op   [api.hanzo.ai, 2026-08-06]",
		liveFlatTools, liveFlatBytes, was)
	t.Logf("  AFTER   this tools/list  %5d tools  %8d bytes  %6.0f B/op", len(tools), len(body), now)
	t.Logf("  ratio                    %5.1fx fewer tools, %.1fx fewer bytes for %.1fx MORE operations (%.0fx per op)",
		float64(liveFlatTools)/float64(len(tools)), float64(liveFlatBytes)/float64(len(body)),
		float64(len(ops))/float64(liveFlatTools), was/now)
	t.Logf("  headroom                 %5d subsystems before a 128-tool client truncates again", slackKeeps-len(tools))
}

// ---------------------------------------------------------------------------
// what a call through one does
// ---------------------------------------------------------------------------

// TestASubsystemToolDispatchesExactlyAsTheFlatCallDid: the envelope is a
// decoding, not a second route. Same child, same handler, same arguments, same
// reply — asserted by comparing the two replies to each other rather than to a
// string, so a change to either path has to change both.
func TestASubsystemToolDispatchesExactlyAsTheFlatCallDid(t *testing.T) {
	kids := map[string]*child{"alpha": start(t, "alpha", 2), "beta": start(t, "beta", 2)}
	h := host(t, []string{"alpha", "beta"}, kids)

	flat := rpc(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call",`+
		`"params":{"name":"beta_opb","arguments":{"which":"x"}}}`)
	grouped := rpc(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call",`+
		`"params":{"name":"beta","arguments":{"op":"beta_opb","input":{"which":"x"}}}}`)

	want, _ := json.Marshal(flat)
	got, _ := json.Marshal(grouped)
	if string(got) != string(want) {
		t.Fatalf("beta{op:beta_opb} answered\n  %s\nand the direct call answered\n  %s", got, want)
	}
	if text := textOf(t, grouped); !strings.Contains(text, `"app":"beta"`) || !strings.Contains(text, `"which":"x"`) {
		t.Fatalf("neither call reached beta's own handler with its arguments: %q", text)
	}
	if isErr, _ := grouped["isError"].(bool); isErr {
		t.Fatalf("the grouped call reported an error: %v", grouped)
	}
}

// textOf is the text of an MCP tool result — the child's own reply, which for
// these fixtures is its JSON output.
func textOf(t *testing.T, res map[string]any) string {
	t.Helper()
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// TestASubsystemToolWithNoOpSaysWhatItNeeds: a model that named the subsystem
// and forgot the operation gets told, and nothing is dispatched.
func TestASubsystemToolWithNoOpSaysWhatItNeeds(t *testing.T) {
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": start(t, "alpha", 1)})
	res := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"alpha","arguments":{}}}`)
	e, ok := res["error"].(map[string]any)
	if !ok {
		t.Fatalf("an envelope with no op must be refused, got %v", res)
	}
	if code, _ := e["code"].(float64); int(code) != -32602 {
		t.Errorf("code = %v, want -32602", e["code"])
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, `"op"`) {
		t.Errorf("the message %q does not tell the model what to send", msg)
	}
}

// TestAnUnservedOpInAnEnvelopeIsRefusedNotForwarded: the envelope does not make
// the door credulous. A name nobody listed is the same -32602 it always was.
func TestAnUnservedOpInAnEnvelopeIsRefusedNotForwarded(t *testing.T) {
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": start(t, "alpha", 1)})
	res := rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call",`+
		`"params":{"name":"alpha","arguments":{"op":"ghost_op","input":{}}}}`)
	if _, refused := res["error"].(map[string]any); !refused {
		t.Fatalf("alpha forwarded an operation nobody serves: %v", res)
	}
}

// ---------------------------------------------------------------------------
// describe
// ---------------------------------------------------------------------------

// TestDescribeReturnsTheOWNERsOwnSchema: the fetch half. What comes back is the
// subsystem's own descriptor — byte for byte what its registry projects — so a
// model that reads it fills the arguments the tool actually declared.
func TestDescribeReturnsTheOWNERsOwnSchema(t *testing.T) {
	kid := start(t, "alpha", 2)
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": kid})

	res := rpc(t, h, `{"jsonrpc":"2.0","id":5,"method":"tools/call",`+
		`"params":{"name":"`+fleet.Describe+`","arguments":{"op":"alpha_opb"}}}`)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s returned no content: %v", fleet.Describe, res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)

	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%s returned %q, which is not a tool descriptor: %v", fleet.Describe, text, err)
	}
	var want map[string]any
	for _, tl := range kid.app.MCPTools() {
		if tl["name"] == "alpha_opb" {
			want = tl
		}
	}
	if want == nil {
		t.Fatal("fixture is wrong: alpha does not serve alpha_opb")
	}
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(gb) != string(wb) {
		t.Fatalf("describe answered\n  %s\nalpha's own registry projects\n  %s", gb, wb)
	}
	// And it is a REAL schema, not a name echoed back: the op's own argument is in it.
	if !strings.Contains(text, `"which"`) {
		t.Errorf("the descriptor carries no input schema: %s", text)
	}
}

// TestDescribeOfANameNobodyServesIsRefused.
func TestDescribeOfANameNobodyServesIsRefused(t *testing.T) {
	h := host(t, []string{"alpha"}, map[string]*child{"alpha": start(t, "alpha", 1)})
	res := rpc(t, h, `{"jsonrpc":"2.0","id":6,"method":"tools/call",`+
		`"params":{"name":"`+fleet.Describe+`","arguments":{"op":"ghost_op"}}}`)
	if _, refused := res["error"].(map[string]any); !refused {
		t.Fatalf("describe answered for an op nobody serves: %v", res)
	}
}

// ---------------------------------------------------------------------------
// the gate, through the new surface
// ---------------------------------------------------------------------------

// TestARefusedOpIsInvisibleUncallableAndUndescribable is the security bar for
// this change, and it is three claims because the grouped surface added two new
// ways to ask.
//
// The child really does serve CreateServiceAccountKey — its registry has it and
// a direct call to the child would mint a key — so what is under test is the
// door's refusal at every path that now exists:
//
//	tools/list        the name is in no subsystem's `op` enum
//	tools/call        console{op:CreateServiceAccountKey} does not run it
//	describe          its schema cannot be read either
//
// All three are the same gate: [fleet.Door.gather] refuses before it writes the
// routing table, and list, call and describe all read that one gathered set.
func TestARefusedOpIsInvisibleUncallableAndUndescribable(t *testing.T) {
	kid := startNamed(t, "console", "CreateServiceAccountKey", "GetUser", "post_v1_chat_completions")
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	// The fixture is only worth something if the child serves it.
	served := false
	for _, tl := range kid.app.MCPTools() {
		if tl["name"] == "CreateServiceAccountKey" {
			served = true
		}
	}
	if !served {
		t.Fatal("fixture is wrong: the child does not serve CreateServiceAccountKey, so refusing it proves nothing")
	}

	// 1. invisible — not in any enum, and not published as a tool of its own.
	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, n := range append(offered(res), names(res)...) {
		if strings.Contains(n, "ServiceAccountKey") {
			t.Errorf("the door offers %q — an agent can mint a credential with it", n)
		}
	}
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), "ServiceAccountKey") {
		t.Errorf("the name survives somewhere in tools/list: %s", raw)
	}
	// …and the surviving siblings are still offered, so this is a gate and not a broken door.
	if got := strings.Join(offered(res), ","); got != "post_v1_chat_completions,GetUser" {
		t.Errorf("the gate ate a surviving op: enum is %q", got)
	}

	// 2. not callable through the subsystem tool.
	call := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"console",`+
		`"arguments":{"op":"CreateServiceAccountKey","input":{"which":"mint"}}}}`)
	e, refused := call["error"].(map[string]any)
	if !refused {
		t.Fatalf("console DISPATCHED CreateServiceAccountKey: %v", call)
	}
	if code, _ := e["code"].(float64); int(code) != -32602 {
		t.Errorf("code = %v, want -32602", e["code"])
	}

	// 3. not describable.
	desc := rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+fleet.Describe+`",`+
		`"arguments":{"op":"CreateServiceAccountKey"}}}`)
	if _, refused := desc["error"].(map[string]any); !refused {
		t.Fatalf("%s handed back the refused op's schema: %v", fleet.Describe, desc)
	}

	// The refusal says nothing a caller could not have guessed. Naming which of
	// "withheld" and "does not exist" it was would make the door an oracle for
	// the surface it just declined to expose.
	for _, res := range []map[string]any{call, desc} {
		e, _ := res["error"].(map[string]any)
		msg, _ := e["message"].(string)
		for _, tell := range []string{"refus", "denied", "polic", "forbid"} {
			if strings.Contains(strings.ToLower(msg), tell) {
				t.Errorf("the refusal message %q distinguishes withheld from absent", msg)
			}
		}
	}

	// And the sibling still runs through the same envelope, so none of the above
	// passes because the door is broken.
	ok := rpc(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"console",`+
		`"arguments":{"op":"post_v1_chat_completions","input":{"which":"hello"}}}}`)
	content, _ := ok["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("the product op did not run through console: %v", ok)
	}
	first, _ := content[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, `"which":"hello"`) {
		t.Fatalf("the grouped call lost its arguments: %q", text)
	}
}
