package main

// ask_test.go — the ask server a run's harness is handed: its ticket is the whole
// of its authority, and ask_user the whole of its surface.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const theTicket = "run_live"

// askApp is the agent app's routes, with one live run whose ticket is theTicket
// and whose person answers "B".
func askApp(t *testing.T, answer func(context.Context) (string, error)) *zip.App {
	t.Helper()
	ph, pa, pk := holds, askFor, keepaliveEvery
	holds = func(tick string) (bool, error) { return tick == theTicket, nil }
	askFor = func(ctx context.Context, tick string, in client.AskIn) (string, error) {
		if tick != theTicket {
			return "", coding.ErrNoTicket
		}
		return answer(ctx)
	}
	keepaliveEvery = 5 * time.Millisecond
	t.Cleanup(func() { holds, askFor, keepaliveEvery = ph, pa, pk })
	app := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	codingEndpoint(app)
	return app
}

func send(t *testing.T, app *zip.App, path, ticket, accept, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://agent"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if ticket != "" {
		req.Header.Set(coding.TicketHeader, ticket)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const call = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}}}`

// The server lists one tool and answers a call with the person's answer, in one
// body or as a stream that holds the connection open while it waits.
func TestTheAskServerServesAskUser(t *testing.T) {
	app := askApp(t, func(context.Context) (string, error) {
		time.Sleep(30 * time.Millisecond)
		return "The person chose: B", nil
	})
	code, body := send(t, app, "/v1/agent/ask", theTicket, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if code != http.StatusOK || !strings.Contains(body, `"protocolVersion":"2025-06-18"`) || !strings.Contains(body, `"tools"`) {
		t.Fatalf("initialize: %d %s", code, body)
	}
	if code, _ := send(t, app, "/v1/agent/ask", theTicket, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("a notification is accepted with no answer, got %d", code)
	}
	_, body = send(t, app, "/v1/agent/ask", theTicket, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(body), &list) != nil || len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "ask_user" {
		t.Fatalf("the server must list ask_user and nothing else: %s", body)
	}
	if _, body := send(t, app, "/v1/agent/ask", theTicket, "application/json", call); !strings.Contains(body, `"text":"The person chose: B"`) || !strings.Contains(body, `"isError":false`) {
		t.Fatalf("a call answered %s", body)
	}
	_, body = send(t, app, "/v1/agent/ask", theTicket, "application/json, text/event-stream", call)
	if !strings.Contains(body, ": waiting") || !strings.Contains(body, "event: message\ndata: ") || !strings.Contains(body, "The person chose: B") {
		t.Fatalf("a streamed call must hold the line open and then answer: %q", body)
	}
}

// The ticket is the server's whole authority, and ask_user its whole surface: no
// ticket or a spent one is 401, another tool is refused, and the ticket opens no
// other route — there it is not a credential at all.
func TestTheTicketOpensAskUserAndNothingElse(t *testing.T) {
	app := askApp(t, func(context.Context) (string, error) { return "unused", nil })
	for _, tick := range []string{"", "run_spent"} {
		if code, _ := send(t, app, "/v1/agent/ask", tick, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
			t.Fatalf("ticket %q: got %d, want 401", tick, code)
		}
	}
	_, body := send(t, app, "/v1/agent/ask", theTicket, "application/json",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_agent_coding","arguments":{}}}`)
	if !strings.Contains(body, `"isError":true`) || strings.Contains(body, "unused") {
		t.Fatalf("the ticket reached a tool other than ask_user: %s", body)
	}
	if code, body := send(t, app, "/v1/agent/coding", theTicket, "", `{"repo":"api","prompt":"x"}`); code < 400 || code >= 500 {
		t.Fatalf("the ticket opened the coding op: %d %s", code, body)
	}
}

// untypedByDesign is the CLOSED list of the routes this composition root adds
// that are NOT typed ops, each with the wire fact that keeps it raw — the half of
// openapi/untyped.json's agent count that lives here (the rest is apps/agents').
var untypedByDesign = map[string]string{
	"POST /v1/agent/ask": "an MCP server: one address answering JSON-RPC, whose body is decided by the " +
		"message's method, and a waiting tools/call answers as an event stream written by a loop that " +
		"outlives the handler (c.SendStreamWriter) — a typed op has one In, one Out and one marshalled value.",
	"POST /v1/agent/mcp/{server}": "an MCP server: one address answering JSON-RPC, whose body is decided by the " +
		"message's method — a notification is a bodiless 202 and an error is a JSON-RPC error object — where a " +
		"typed op has one In, one Out and one marshalled value.",
	"GET /v1/agent/coding/{session}/artifacts/{wildcard1}": "a stored artifact's own bytes, streamed as text or " +
		"application/octet-stream, where a typed op marshals one JSON value.",
}

// TestEveryRouteHereIsTypedOrNamed holds the two ledgers to the routes this
// composition root serves, so a route added raw here goes red.
func TestEveryRouteHereIsTypedOrNamed(t *testing.T) {
	app := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	codingEndpoint(app)
	doc, err := spec.Of(app, spec.Info{Title: "agent", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range served {
		if _, typed := reg.Ops[key]; !typed && untypedByDesign[key] == "" {
			t.Errorf("%s is served raw and named nowhere", key)
		}
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which is not served", key)
		}
		if _, typed := reg.Ops[key]; typed {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}
