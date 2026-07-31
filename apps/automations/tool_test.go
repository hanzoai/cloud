package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/tools"
)

// This subsystem serves NO tool door of its own. Every connector action reaches a
// caller through connectorToolProvider — the ONE projection, registered into the
// unified tool plane at Mount — so these tests exercise that provider directly.
// It is what POST /v1/tools/call dispatches through, and therefore what the
// fleet's one MCP door reaches.

// TestConnectorToolsArePublished: every connector action is published as a
// "<connector>_<action>" tool with a derived input schema.
func TestConnectorToolsArePublished(t *testing.T) {
	newApp(t)
	list, err := connectorToolProvider{}.List(context.Background(), tools.Scope{Org: "acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]json.RawMessage{}
	for _, tl := range list {
		names[tl.Name] = tl.Schema
	}
	for _, want := range []string{"slack_send_message", "core_http_request", "core_code", "github_create_issue", "google_append_row", "google_list_files"} {
		schema, ok := names[want]
		if !ok {
			t.Fatalf("the tool plane is missing %q (%d tools published)", want, len(names))
		}
		if len(schema) == 0 || !strings.Contains(string(schema), `"type":"object"`) {
			t.Errorf("%s has no object input schema: %s", want, schema)
		}
	}
}

// TestConnectorToolDispatches: a dispatch through the plane runs the action's Run
// end-to-end (core_code echoes its resolved input), bound to the caller's org.
func TestConnectorToolDispatches(t *testing.T) {
	newApp(t)
	out, err := connectorToolProvider{}.Dispatch(context.Background(),
		tools.Principal{Org: "acme"}, "core_code", map[string]any{"greeting": "hello"})
	if err != nil {
		t.Fatalf("dispatch core_code: %v", err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"greeting":"hello"`) {
		t.Fatalf("core_code must echo its input, got %s", b)
	}
}

// TestConnectorToolFailsClosed: invoking slack_send_message with no connection
// (integrations not mounted) fails closed with an honest error — never a fake
// success and never another tenant's token.
func TestConnectorToolFailsClosed(t *testing.T) {
	newApp(t)
	_, err := connectorToolProvider{}.Dispatch(context.Background(),
		tools.Principal{Org: "acme"}, "slack_send_message", map[string]any{"channel": "C1", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "slack not connected") {
		t.Fatalf("slack must fail closed with 'slack not connected', got %v", err)
	}
}

// TestConnectorToolUnknownName: a name no connector answers is ErrUnknownTool, so
// the plane reports the miss rather than dispatching something else.
func TestConnectorToolUnknownName(t *testing.T) {
	newApp(t)
	_, err := connectorToolProvider{}.Dispatch(context.Background(),
		tools.Principal{Org: "acme"}, "no_such_tool", nil)
	if !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("unknown tool must be ErrUnknownTool, got %v", err)
	}
}

// TestInvokeToolRequiresAValidatedOrg: the in-process seam refuses a caller with
// no validated org — the dispatch pins every credential to it, so an unnamed
// caller has no scope to be confined to.
func TestInvokeToolRequiresAValidatedOrg(t *testing.T) {
	newApp(t)
	if _, err := InvokeTool(context.Background(), "", "core_code", nil); err == nil {
		t.Fatalf("InvokeTool with no org must refuse")
	}
}
