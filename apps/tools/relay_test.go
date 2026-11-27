package tools

// relay_test.go — a run's MCP request reaches its org's server from HERE: the
// credential is read from KMS and added on this side, the server is dialled
// through the guarded client, and only a server an admin registered and the tools
// of it an admin activated are answered.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/outbound"
	"github.com/zap-proto/zip"
)

// upstream is an org's MCP server, recording the credential each request carried.
type mcpUpstream struct {
	mu    sync.Mutex
	seen  []string // the Authorization of every request to the docs server
	calls []string // the tool of every tools/call
}

func (u *mcpUpstream) serve(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		if r.URL.Path != "/legacy" {
			u.seen = append(u.seen, r.Header.Get("Authorization"))
		}
		if req.Method == "tools/call" {
			u.calls = append(u.calls, req.Params.Name)
		}
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "echoes", "inputSchema": map[string]any{"type": "object"}},
				{"name": "wipe", "description": "wipes everything", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echoed"}}, "echoed": req.Params.Arguments,
			}})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// relaying mounts the plane with one admitted server at url, whose credential
// KMS holds, and echo — not wipe — activated by an admin; every call is offered
// to the charger it answers, as every dispatch is.
func relaying(t *testing.T, url string) *fakeCharger {
	t.Helper()
	newApp(t, nil)
	ch := &fakeCharger{}
	std.SetCharger(ch)
	ctx := context.Background()
	kms := fakeKMS{"docs": "Bearer sekret"}
	mounted.State.kms, mounted.State.mcp.kms = kms, kms
	for _, srv := range []MCPServer{
		{ID: "docs", Org: "acme", Name: "Docs", URL: url, AuthHeader: "Authorization", HasSecret: true, Admitted: true},
		{ID: "legacy", Org: "acme", Name: "Legacy", URL: url + "/legacy"},
	} {
		if _, err := mounted.State.servers.Write(ctx, srv, true); err != nil {
			t.Fatal(err)
		}
	}
	for name, admitted := range map[string]bool{"docs_echo": true, "docs_wipe": false, "legacy_echo": true} {
		if err := mounted.State.activation.Activate(ctx, "acme", "", name, SourceMCP, "u", admitted); err != nil {
			t.Fatal(err)
		}
	}
	return ch
}

// run is the plane context a run's relay acts on: the run's person in its org.
func run(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "sub-zoe"})
}

// A RELAYED CALL reaches the server with the credential added here, and answers
// the server's result verbatim; the list is the tools an admin activated.
func TestARunsCallReachesItsServerWithTheCredentialAddedHere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		up := &mcpUpstream{}
		ts := up.serve(t)
		ch := relaying(t, ts.URL)
		mounted.State.mcp.http = ts.Client() // the loopback test server; the guard is held below

		listed, err := planeRelay(run("acme"), &client.RelayIn{Server: "docs", Method: "tools/list"})
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		var list struct {
			Tools []struct{ Name string } `json:"tools"`
		}
		if json.Unmarshal(listed.Result, &list) != nil || len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
			t.Fatalf("the run was listed %s, want echo alone", listed.Result)
		}
		out, err := planeRelay(run("acme"), &client.RelayIn{Server: "docs", Method: "tools/call",
			Params: json.RawMessage(`{"name":"echo","arguments":{"hi":"there"}}`)})
		if err != nil {
			t.Fatalf("tools/call: %v", err)
		}
		if !strings.Contains(string(out.Result), `"echoed":{"hi":"there"}`) || !strings.Contains(string(out.Result), `"text":"echoed"`) {
			t.Fatalf("the call answered %s", out.Result)
		}
		if want := client.ToolResource("mcp", "docs_echo"); len(ch.charged) != 1 || ch.charged[0] != want {
			t.Fatalf("the call was offered to the charger as %v, want %s once", ch.charged, want)
		}
		up.mu.Lock()
		defer up.mu.Unlock()
		if len(up.calls) != 1 || up.calls[0] != "echo" {
			t.Fatalf("the server was called for %v", up.calls)
		}
		for i, a := range up.seen {
			if a != "Bearer sekret" {
				t.Fatalf("request %d reached the server with %q, not the credential KMS holds", i, a)
			}
		}
	})
}

// What a run does not carry is not answered: a tool no admin activated, a server
// no admin registered, a server the org does not have, another org's server, a
// method a harness does not ask, a call with no org, and a tool for sale — a run
// has no way to pay, and a paid tool is never served free.
func TestTheRelayAnswersOnlyWhatARunCarries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		up := &mcpUpstream{}
		ts := up.serve(t)
		relaying(t, ts.URL)
		mounted.State.mcp.http = ts.Client()
		std.SetCharger(&fakeCharger{err: ErrPaymentRequired})
		for _, c := range []struct {
			name string
			ctx  context.Context
			in   client.RelayIn
			want int
		}{
			{"a tool no admin activated", run("acme"), client.RelayIn{Server: "docs", Method: "tools/call", Params: json.RawMessage(`{"name":"wipe"}`)}, http.StatusForbidden},
			{"a server no admin registered", run("acme"), client.RelayIn{Server: "legacy", Method: "tools/list"}, http.StatusForbidden},
			{"a server the org does not have", run("acme"), client.RelayIn{Server: "evil", Method: "tools/list"}, http.StatusNotFound},
			{"another org's server", run("globex"), client.RelayIn{Server: "docs", Method: "tools/call", Params: json.RawMessage(`{"name":"echo"}`)}, http.StatusNotFound},
			{"a method a harness does not ask", run("acme"), client.RelayIn{Server: "docs", Method: "resources/read"}, http.StatusBadRequest},
			{"a call acting for no org", context.Background(), client.RelayIn{Server: "docs", Method: "tools/list"}, http.StatusForbidden},
			{"a tool for sale", run("acme"), client.RelayIn{Server: "docs", Method: "tools/call", Params: json.RawMessage(`{"name":"echo"}`)}, http.StatusPaymentRequired},
		} {
			if _, err := planeRelay(c.ctx, &c.in); status(err) != c.want {
				t.Errorf("%s = %v, want %d", c.name, err, c.want)
			}
		}
		up.mu.Lock()
		defer up.mu.Unlock()
		if len(up.calls) != 0 {
			t.Fatalf("a refused call reached the server: %v", up.calls)
		}
	})
}

// The server is dialled through the guarded client: an address that resolves
// inside our network is refused at the dial, however it came to be on the row.
func TestTheRelayDialsThroughTheGuard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		up := &mcpUpstream{}
		ts := up.serve(t)
		relaying(t, ts.URL)
		_, err := planeRelay(run("acme"), &client.RelayIn{Server: "docs", Method: "tools/list"})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusBadGateway || !strings.Contains(err.Error(), outbound.ErrRefused.Error()) {
			t.Fatalf("a loopback server through the guard = %v, want the dial refused", err)
		}
		up.mu.Lock()
		defer up.mu.Unlock()
		if len(up.seen) != 0 {
			t.Fatalf("the guarded client reached a loopback server %d times", len(up.seen))
		}
	})
}

// TestTheRelayEnforcesUserMutesAndAuditsEveryDecisionWithRedactedIdentity proves
// P1 #4 and P1 #5: a tool or server muted by the run's person cannot be invoked
// at planeRelay even if activated by an org admin, and every permitted or
// denied relay call is recorded in the audit trail with destination host,
// policy decision ("allow" or "deny"), run session ID, and redacted identity.
func TestTheRelayEnforcesUserMutesAndAuditsEveryDecisionWithRedactedIdentity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		up := &mcpUpstream{}
		ts := up.serve(t)
		relaying(t, ts.URL)
		mounted.State.mcp.http = ts.Client()

		rec, err := audit.Open(t.TempDir(), "audit-relay", nil)
		if err != nil {
			t.Fatalf("audit open: %v", err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		mounted.State.audit = rec

		ctx := run("acme")
		const sessID = "sess_audit_run_42"

		// 1. Permitted call to docs_echo before muting -> allow.
		if _, err := planeRelay(ctx, &client.RelayIn{
			Server: "docs", Method: "tools/call", Session: sessID,
			Params: json.RawMessage(`{"name":"echo","arguments":{"step":1}}`),
		}); err != nil {
			t.Fatalf("initial echo call failed: %v", err)
		}

		// 2. User "sub-zoe" mutes tool "docs_echo" in their personal kit.
		if err := mounted.State.activation.Mute(ctx, "acme", "sub-zoe", "docs_echo"); err != nil {
			t.Fatalf("mute docs_echo: %v", err)
		}
		// tools/list must now exclude "echo".
		listed, err := planeRelay(ctx, &client.RelayIn{
			Server: "docs", Method: "tools/list", Session: sessID,
		})
		if err != nil {
			t.Fatalf("tools/list after mute: %v", err)
		}
		if strings.Contains(string(listed.Result), `"echo"`) {
			t.Fatalf("muted tool echo still appeared in tools/list: %s", listed.Result)
		}
		// Direct tools/call to muted "echo" must be refused 403 Forbidden.
		if _, err := planeRelay(ctx, &client.RelayIn{
			Server: "docs", Method: "tools/call", Session: sessID,
			Params: json.RawMessage(`{"name":"echo","arguments":{"step":2}}`),
		}); status(err) != http.StatusForbidden {
			t.Fatalf("muted tool echo was not refused 403: %v", err)
		}

		// 3. User "sub-zoe" mutes the entire server "docs".
		if err := mounted.State.activation.Mute(ctx, "acme", "sub-zoe", "docs"); err != nil {
			t.Fatalf("mute server docs: %v", err)
		}
		if _, err := planeRelay(ctx, &client.RelayIn{
			Server: "docs", Method: "tools/list", Session: sessID,
		}); status(err) != http.StatusForbidden {
			t.Fatalf("muted server docs was not refused 403: %v", err)
		}

		// 4. Verify the audit trail captured every allow and deny decision with
		// destination host, run session ID, and redacted identity (never raw "sub-zoe").
		rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "acme"})
		if err != nil {
			t.Fatalf("query audit trail: %v", err)
		}
		if len(rows) != 4 {
			t.Fatalf("expected 4 audit records (2 allow, 2 deny), got %d", len(rows))
		}
		wantSub := redactActorSub("sub-zoe")
		wantDest := destinationHost(ts.URL)
		var allows, denies int
		for _, r := range rows {
			if r.Actor.Sub == "sub-zoe" || r.Actor.Sub != wantSub {
				t.Errorf("audit record Actor.Sub = %q, want redacted %q", r.Actor.Sub, wantSub)
			}
			if r.RequestID != sessID {
				t.Errorf("audit record RequestID = %q, want session %q", r.RequestID, sessID)
			}
			if r.Path != wantDest {
				t.Errorf("audit record destination Path = %q, want %q", r.Path, wantDest)
			}
			switch r.Outcome.Result {
			case "allow":
				allows++
			case "deny":
				denies++
			default:
				t.Errorf("unexpected Outcome.Result = %q", r.Outcome.Result)
			}
		}
		if allows != 2 || denies != 2 {
			t.Fatalf("audit counts allow=%d deny=%d, want 2 and 2", allows, denies)
		}
	})
}
