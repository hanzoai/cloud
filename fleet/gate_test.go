// Copyright © 2026 Hanzo AI. MIT License.

package fleet_test

// The gate, over the wire, with a child that really does serve the dangerous op.
//
// These tests are worth more than the unit tests beside them in exactly one way:
// the child here SERVES CreateServiceAccountKey. Its registry has the op, its
// own /mcp lists it, and a direct call to the child would run it. So what is
// being tested is the door's REFUSAL, not the absence of the op — which is the
// difference between a policy and a coincidence, and the difference the live
// server did not have when it projected 1,323 tools with no auth at all.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/fleet"
	"github.com/zap-proto/zip"
)

// startNamed brings up a child serving exactly the ops named, one route each.
// The names are real fleet operation ids, so the child is a faithful stand-in
// for the subsystem that serves them.
func startNamed(t *testing.T, name string, ops ...string) *child {
	t.Helper()
	sock := filepath.Join(t.TempDir(), name+".sock")
	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	for i, id := range ops {
		route := "/v1/" + name + "/op" + string(rune('a'+i))
		zip.Post(app, route, func(_ context.Context, in *thingIn) (*thingOut, error) {
			return &thingOut{App: name, Which: in.Which}, nil
		}, zip.WithOperationID(id), zip.WithSummary("what "+name+" does at "+id))
	}
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, sock)
	return &child{name: name, addr: sock, app: app}
}

// order is the operations the door offers, IN THE ORDER IT OFFERS THEM — unlike
// listed(), which sorts. The order is the mechanism under test in
// TestTheProductSurfaceLeadsTheList, so sorting it away would test nothing.
func order(t *testing.T, h *zip.App) []string {
	t.Helper()
	return offered(rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
}

// dangerous and useful are the two halves, as one child each would serve them.
var dangerous = []string{
	"CreateServiceAccountKey", "CreateSessionByEmailPassword", "CreateResetPasswordToken",
	"GetResetPasswordToken", "CreateUser", "DeleteUser", "CreateAuthDomain",
	"CreateRole", "SetRoleByUserID", "CreateInvite", "getToken", "get_kms_secrets",
}

var useful = []string{
	"post_chat_completions", "get_models", "post_embeddings",
	"post_agents_sessions_by_id_message", "post_code_ask", "post_git_repos",
	"post_deploy_applications_by_name_sync", "GetUser", "GetUserPreference",
}

// TestTheDoorDoesNotProjectACredentialOpItsChildServes.
func TestTheDoorDoesNotProjectACredentialOpItsChildServes(t *testing.T) {
	kid := startNamed(t, "console", append(append([]string{}, dangerous...), useful...)...)
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	// The child really does serve all of them — otherwise this proves nothing.
	served := map[string]bool{}
	for _, tl := range kid.app.MCPTools() {
		served[tl["name"].(string)] = true
	}
	for _, n := range dangerous {
		if !served[n] {
			t.Fatalf("fixture is wrong: the child does not serve %q, so refusing it at the door proves nothing", n)
		}
	}

	got := map[string]bool{}
	for _, n := range order(t, h) {
		got[n] = true
	}
	// The enum carries the name the door PUBLISHES, so both halves are asked in
	// that spelling — and the dangerous half is asked in both, because a refused
	// operation must not reappear under a friendlier name.
	for _, n := range dangerous {
		if got[n] || got[fleet.Phrase(n)] {
			t.Errorf("the door PROJECTED %q — an agent can mint or read a credential with it", n)
		}
	}
	for _, n := range useful {
		if !got[fleet.Phrase(n)] {
			t.Errorf("the door dropped %q (offered as %q) — the gate ate a product tool", n, fleet.Phrase(n))
		}
	}
}

// TestARefusedToolIsNotCALLABLE is the half that makes this a boundary.
//
// A filter applied only to tools/list would leave every already-cached name
// callable — and Slack has 128 of them cached right now. The refusal lives in
// gather(), which is where the tool→app routing table is written, so a refused
// name is never written and a call naming it cannot be dispatched. This test
// calls a tool that DOES exist in the child, with no tools/list first, so the
// door must refuse it on the discovery path rather than from a stale table.
func TestARefusedToolIsNotCALLABLE(t *testing.T) {
	kid := startNamed(t, "console", "CreateServiceAccountKey", "post_chat_completions")
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	res := rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call",`+
		`"params":{"name":"CreateServiceAccountKey","arguments":{"which":"mint"}}}`)
	e, refused := res["error"].(map[string]any)
	if !refused {
		t.Fatalf("tools/call CreateServiceAccountKey was DISPATCHED: %v", res)
	}
	if code, _ := e["code"].(float64); int(code) != -32602 {
		t.Errorf("code = %v, want -32602", e["code"])
	}
	if msg, _ := e["message"].(string); strings.Contains(strings.ToLower(msg), "refus") ||
		strings.Contains(strings.ToLower(msg), "denied") ||
		strings.Contains(strings.ToLower(msg), "polic") {
		t.Errorf("the refusal message %q distinguishes 'withheld' from 'does not exist', "+
			"which makes the door an oracle for the surface it just declined to expose", msg)
	}

	// …and the surviving sibling still runs, so the test is not passing because
	// the door is broken.
	ok := rpc(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call",`+
		`"params":{"name":"post_chat_completions","arguments":{"which":"hello"}}}`)
	content, _ := ok["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("the product tool did not run: %v", ok)
	}
	first, _ := content[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, `"which":"hello"`) {
		t.Fatalf("tools/call post_chat_completions lost its arguments: %q", text)
	}
}

// TestTheProductSurfaceLeadsTheList is mechanism (b) end to end.
//
// The child's own projection is sorted by name (zip mcpTools), and by that order
// every PascalCase console op precedes every product op — 'C' < 'p' in ASCII.
// The door must reverse that.
func TestTheProductSurfaceLeadsTheList(t *testing.T) {
	kid := startNamed(t, "console",
		"AgentCheckIn", "GetAlerts", "GetUser", "GetUserPreference", "AuthzCheck",
		"post_chat_completions", "get_models", "post_code_ask")
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	got := order(t, h)
	if len(got) == 0 {
		t.Fatal("the door listed nothing")
	}
	if got[0] != fleet.Phrase("post_chat_completions") {
		t.Errorf("the first tool is %q; chat leads the product surface", got[0])
	}
	// Ranking reads the ROUTE and the enum carries the phrase, so a lookup names
	// the operation the way the fixture declared it and finds it the way the door
	// published it. That the two agree for every entry is the point.
	at := func(id string) int {
		for i, n := range got {
			if n == fleet.Phrase(id) {
				return i
			}
		}
		t.Fatalf("%q (offered as %q) is missing from %v", id, fleet.Phrase(id), got)
		return -1
	}
	for _, product := range []string{"post_chat_completions", "get_models", "post_code_ask"} {
		for _, console := range []string{"AgentCheckIn", "GetAlerts", "GetUser", "GetUserPreference", "AuthzCheck"} {
			if at(product) > at(console) {
				t.Errorf("%q (%d) comes after %q (%d) — a truncating client keeps the console, not the product",
					product, at(product), console, at(console))
			}
		}
	}
	// Within a bucket the order is still alphabetical, so the list is stable.
	if at("AgentCheckIn") > at("AuthzCheck") {
		t.Error("the tail is not alphabetical; a list that reorders itself churns every client's cache")
	}
}

// TestTheDoorSAYSHowMuchItWithheld.
//
// This package's whole thesis is that a silently shortened list is the same
// defect as a stale one: the caller cannot tell "serves nothing" from "did not
// answer" — or, now, from "was not allowed to say". A policy that shortens the
// list quietly would reintroduce exactly that, so the count and the rule ride on
// _meta beside the outage report.
func TestTheDoorSAYSHowMuchItWithheld(t *testing.T) {
	kid := startNamed(t, "console", append(append([]string{}, dangerous...), useful...)...)
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	meta, ok := res["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list withheld %d tools and said nothing: %v", len(dangerous), res)
	}
	row, ok := meta[fleet.Refused].(map[string]any)
	if !ok {
		t.Fatalf("_meta has no %q: %v", fleet.Refused, meta)
	}
	if n, _ := row["count"].(float64); int(n) != len(dangerous) {
		t.Errorf("_meta[%q].count = %v, want %d", fleet.Refused, row["count"], len(dangerous))
	}
	if rule, _ := row["rule"].(string); rule != fleet.TheRule {
		t.Errorf("_meta[%q].rule = %q; an operator who wonders where a tool went must be able to read why", fleet.Refused, rule)
	}
}

// TestRefusalAndOutageAreREPORTEDTOGETHER: the two _meta keys are independent
// facts about one answer and must not overwrite each other. They did, in the
// first draft of this change — one map literal, assigned twice.
func TestRefusalAndOutageAreREPORTEDTOGETHER(t *testing.T) {
	kids := map[string]*child{"console": startNamed(t, "console", "CreateUser", "post_chat_completions")}
	h := host(t, []string{"console", "beta"}, kids) // beta has no instance

	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	meta, _ := res["_meta"].(map[string]any)
	if _, ok := meta[fleet.Refused]; !ok {
		t.Errorf("_meta lost the refusal report: %v", meta)
	}
	if _, ok := meta[fleet.Unavailable]; !ok {
		t.Errorf("_meta lost the outage report: %v", meta)
	}
}

// TestAQuietFleetReportsNoMetaAtAll: the keys appear only when they say
// something. A `_meta` present on every answer is noise a client learns to skip.
func TestAQuietFleetReportsNoMetaAtAll(t *testing.T) {
	kids := map[string]*child{"ai": startNamed(t, "ai", "post_chat_completions", "get_models")}
	h := host(t, []string{"ai"}, kids)
	res := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if m, present := res["_meta"]; present {
		t.Errorf("nothing was withheld and nothing was down, but _meta = %v", m)
	}
}

// TestTheGateIsNotAHeaderTrick: the child answers tools/list for the CALLER, so
// a client could try to influence what it is offered. Nothing about the door's
// refusal reads the request, and this pins that: the same fleet, asked with a
// bearer token, an admin-looking header, and nothing at all, projects the same
// tools. (It also documents the finding this change did NOT fix — see the note
// on transport auth in the report: a bogus bearer is accepted today because
// there is no authentication at this door at all.)
func TestTheGateIsNotAHeaderTrick(t *testing.T) {
	kid := startNamed(t, "console", append(append([]string{}, dangerous...), useful...)...)
	h := host(t, []string{"console"}, map[string]*child{"console": kid})

	base := order(t, h)
	for _, hdr := range [][2]string{
		{"Authorization", "Bearer definitely-not-a-real-token"},
		{"X-Hanzo-Admin", "true"},
		{"X-Forwarded-User", "z@hanzo.ai"},
	} {
		req, err := http.NewRequest("POST", "http://cloud/v1/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(hdr[0], hdr[1])
		resp, err := h.Test(req, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var env struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: %v", hdr[0], err)
		}
		got := offered(env.Result)
		if strings.Join(got, ",") != strings.Join(base, ",") {
			t.Errorf("%s: %v changed the projection to %v", hdr[0], hdr[1], got)
		}
	}
}
