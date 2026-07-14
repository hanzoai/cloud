package automations

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMCPToolsList: tools/list (with a validated principal) exposes every connector
// action as a "<connector>_<action>" tool.
func TestMCPToolsList(t *testing.T) {
	app := newApp(t)
	r := reqRaw(t, app, "/v1/automations/mcp", "acme", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if r.Code != 200 {
		t.Fatalf("tools/list want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("tools/list body: %v (%s)", err, r.Body)
	}
	names := map[string]bool{}
	for _, tl := range out.Result.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"slack_send_message", "core_http_request", "core_code", "github_create_issue", "google_append_row", "google_list_files"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q; got %v", want, names)
		}
	}
}

// TestMCPToolCallGated: tools/call with NO validated principal is refused 403 — the
// tool plane never serves an unauthenticated caller.
func TestMCPToolCallGated(t *testing.T) {
	app := newApp(t)
	r := reqRaw(t, app, "/v1/automations/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"core_code","arguments":{"x":1}}}`)
	if r.Code != 403 {
		t.Fatalf("tools/call without principal want 403, got %d (%s)", r.Code, r.Body)
	}
}

// TestMCPToolCallDispatches: tools/call with a principal dispatches to the action's
// Run end-to-end (core_code echoes its resolved input).
func TestMCPToolCallDispatches(t *testing.T) {
	app := newApp(t)
	r := reqRaw(t, app, "/v1/automations/mcp", "acme",
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"core_code","arguments":{"greeting":"hello"}}}`)
	if r.Code != 200 {
		t.Fatalf("tools/call want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("tools/call body: %v (%s)", err, r.Body)
	}
	if out.Error != nil {
		t.Fatalf("tools/call unexpected error: %s", out.Error.Message)
	}
	if len(out.Result.Content) == 0 || !strings.Contains(out.Result.Content[0].Text, `"greeting":"hello"`) {
		t.Fatalf("core_code must echo its input, got %+v", out.Result.Content)
	}
}

// TestMCPToolCallSlackFailsClosed: invoking slack_send_message with no connection
// (integrations not mounted) fails closed with an honest error — never a fake success
// and never another tenant's token.
func TestMCPToolCallSlackFailsClosed(t *testing.T) {
	app := newApp(t)
	r := reqRaw(t, app, "/v1/automations/mcp", "acme",
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"slack_send_message","arguments":{"channel":"C1","text":"hi"}}}`)
	if r.Code != 200 {
		t.Fatalf("tools/call want 200 (JSON-RPC error in body), got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &out)
	if out.Error == nil || !strings.Contains(out.Error.Message, "slack not connected") {
		t.Fatalf("slack must fail closed with 'slack not connected', got %s", r.Body)
	}
}
