package main

// relay_test.go — the MCP address a run's harness reaches its org's servers
// through: the ticket and the server it names are its whole authority, and what
// it relays is the tool app's answer, verbatim.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/coding"
	"github.com/zap-proto/zip"
)

// relayed is one request relayFor was asked.
type relayed struct {
	ticket, server, method, params string
	stated                         zip.Caller
}

// relayApp is the agent app's routes, with one live run whose ticket is theTicket
// and whose kit carries the server docs.
func relayApp(t *testing.T, answer func(method string) (json.RawMessage, error)) (*zip.App, *[]relayed) {
	t.Helper()
	pc, pr := carried, relayFor
	var asked []relayed
	carried = func(tick, server string) error {
		switch {
		case tick != theTicket:
			return coding.ErrNoTicket
		case server != "docs":
			return coding.ErrNotCarried
		}
		return nil
	}
	relayFor = func(ctx context.Context, tick, server, method string, params json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, relayed{tick, server, method, string(params), zip.CallerOf(ctx)})
		if err := carried(tick, server); err != nil {
			return nil, err
		}
		return answer(method)
	}
	t.Cleanup(func() { carried, relayFor = pc, pr })
	app := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	codingEndpoint(app)
	return app, &asked
}

// The server a run's kit carries answers MCP: it initializes as that server,
// accepts a notification, and relays tools/list and tools/call as the tool app
// answered them.
func TestTheRelayServesTheRunsServer(t *testing.T) {
	app, asked := relayApp(t, func(method string) (json.RawMessage, error) {
		if method == "tools/list" {
			return json.RawMessage(`{"tools":[{"name":"search"}]}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"found"}],"isError":false}`), nil
	})
	code, body := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if code != http.StatusOK || !strings.Contains(body, `"name":"docs"`) || !strings.Contains(body, `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize: %d %s", code, body)
	}
	if code, _ := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("a notification is accepted with no answer, got %d", code)
	}
	if _, body := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); body != `{"id":2,"jsonrpc":"2.0","result":{"tools":[{"name":"search"}]}}` {
		t.Fatalf("tools/list answered %s", body)
	}
	_, body = send(t, app, "/v1/agent/mcp/docs", theTicket, "application/json, text/event-stream",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"}}}`)
	if !strings.Contains(body, `"text":"found"`) || !strings.Contains(body, `"id":3`) {
		t.Fatalf("tools/call answered %s", body)
	}
	if len(*asked) != 2 || (*asked)[1].method != "tools/call" || (*asked)[1].params != `{"name":"search","arguments":{"q":"x"}}` {
		t.Fatalf("the relay was asked %+v", *asked)
	}
	// The harness's request says nothing about who the run is: nothing of it rides on.
	if who := (*asked)[1].stated; who != (zip.Caller{}) {
		t.Fatalf("the relay was handed the harness's caller %+v", who)
	}
}

// The ticket and the server are the address's whole authority: no ticket, a spent
// one, and a server the run's kit does not carry are refused before anything is
// relayed; a relay the tool app refused is a JSON-RPC error, never a result.
func TestTheRelayOpensOnlyTheRunsOwnServers(t *testing.T) {
	app, asked := relayApp(t, func(string) (json.RawMessage, error) {
		return nil, zip.ErrForbidden("tools relay: no admin of the org activated docs's wipe")
	})
	for _, c := range []struct {
		name, path, ticket string
		want               int
	}{
		{"no ticket", "/v1/agent/mcp/docs", "", http.StatusUnauthorized},
		{"a spent ticket", "/v1/agent/mcp/docs", "run_spent", http.StatusUnauthorized},
		{"a server the kit does not carry", "/v1/agent/mcp/stripe-com", theTicket, http.StatusForbidden},
	} {
		for _, method := range []string{"initialize", "tools/list", "tools/call"} {
			code, body := send(t, app, c.path, c.ticket, "", `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"name":"x"}}`)
			if code != c.want || !strings.Contains(body, `"error"`) {
				t.Fatalf("%s, %s: %d %s, want %d", c.name, method, code, body, c.want)
			}
		}
	}
	if len(*asked) != 0 {
		t.Fatalf("a refused request was relayed: %+v", *asked)
	}
	_, body := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wipe"}}`)
	if !strings.Contains(body, `"error"`) || strings.Contains(body, `"result"`) || !strings.Contains(body, "no admin of the org activated") {
		t.Fatalf("a refused call answered %s", body)
	}
	if _, body := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`); !strings.Contains(body, "-32601") {
		t.Fatalf("a method the relay does not carry answered %s", body)
	}
}

// A request past the run's relay bound is a 429 the harness can wait out, and
// nothing is relayed for it.
func TestTheRelayAnswersARunPastItsBound(t *testing.T) {
	app, _ := relayApp(t, func(string) (json.RawMessage, error) {
		return nil, fmt.Errorf("%w: 4 requests are in flight", coding.ErrRelayLimit)
	})
	req, _ := http.NewRequest(http.MethodPost, "http://agent/v1/agent/mcp/docs",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"search"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(coding.TicketHeader, theTicket)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" ||
		!strings.Contains(string(b), `"error"`) || strings.Contains(string(b), `"result"`) {
		t.Fatalf("a run past its bound: %d %q %s", resp.StatusCode, resp.Header.Get("Retry-After"), b)
	}
}

// A relayed request that failed on the way — not an answer from the tool app —
// is told to the harness as the server not being reached. Its text may spell an
// address with a key in its query, and the harness runs in a sandbox its person
// can open.
func TestTheRelayNamesNoAddressWhenItFails(t *testing.T) {
	app, _ := relayApp(t, func(string) (json.RawMessage, error) {
		return nil, errors.New(`Post "https://mcp.example.com/rpc?key=SECRET_IN_QUERY": dial tcp: i/o timeout`)
	})
	_, body := send(t, app, "/v1/agent/mcp/docs", theTicket, "", `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"search"}}`)
	if strings.Contains(body, "SECRET") || strings.Contains(body, "mcp.example.com") || !strings.Contains(body, "upstream unreachable") {
		t.Fatalf("a failed relay answered %s", body)
	}
}
