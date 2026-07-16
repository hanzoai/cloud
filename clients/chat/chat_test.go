package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/tools"
	luxlog "github.com/luxfi/log"
	openai "github.com/sashabaranov/go-openai"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// newApp mounts chat on a fresh zip.App. The ai completion path and the tool
// registry are stubbed on the mounted service per test — no live model or plane is
// wired, so the tests exercise the round assembly + the action/ops split in
// isolation. list is stubbed to empty so the round never depends on the process-wide
// registry.
func newApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), AIDefaultModel: "zen"}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	mounted.State.list = func(context.Context, tools.Scope) []tools.Tool { return nil }
	return app
}

// req issues one POST /v1/chat. org != "" sets the validated identity headers
// (X-Org-Id + X-User-Id) exactly as SanitizeIdentity would; org == "" sends none —
// the anonymous path the 403 test needs.
func req(t *testing.T, app *zip.App, org string, body any) (int, chatResponse) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(http.MethodPost, "/v1/chat", r)
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test POST /v1/chat: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out chatResponse
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// completion builds a fake assistant completion: optional text plus zero or more
// tool calls (each with a trivial argument object).
func completion(content string, toolCalls ...string) openai.ChatCompletionResponse {
	msg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: content}
	for i, name := range toolCalls {
		msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
			ID:       fmt.Sprintf("call_%d", i),
			Type:     openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: name, Arguments: `{"x":1}`},
		})
	}
	return openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: msg}}}
}

func names[T any](xs []T, name func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, name(x))
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChatRound is the table test for the tool-calling round: a registry tool call
// dispatches to actions; an unknown call falls to ops; plain content is the reply; a
// graph-capability call is always an op (advisory); an unknown capability is 400.
func TestChatRound(t *testing.T) {
	cases := []struct {
		name           string
		capability     string
		completion     openai.ChatCompletionResponse
		known          map[string]bool
		wantStatus     int
		wantReply      string
		wantActions    []string
		wantOps        []string
		wantDispatched []string
	}{
		{
			name:           "known registry tool dispatched to actions",
			capability:     "create",
			completion:     completion("", "render_image"),
			known:          map[string]bool{"render_image": true},
			wantStatus:     http.StatusOK,
			wantActions:    []string{"render_image"},
			wantOps:        []string{},
			wantDispatched: []string{"render_image"},
		},
		{
			name:           "unknown tool falls to ops",
			capability:     "create",
			completion:     completion("", "frobnicate"),
			known:          map[string]bool{},
			wantStatus:     http.StatusOK,
			wantActions:    []string{},
			wantOps:        []string{"frobnicate"},
			wantDispatched: []string{},
		},
		{
			name:           "plain content is the reply",
			capability:     "graph",
			completion:     completion("here is your graph plan"),
			known:          map[string]bool{},
			wantStatus:     http.StatusOK,
			wantReply:      "here is your graph plan",
			wantActions:    []string{},
			wantOps:        []string{},
			wantDispatched: []string{},
		},
		{
			name:           "graph capability calls are advisory ops, never dispatched",
			capability:     "graph",
			completion:     completion("", "add_node"),
			known:          map[string]bool{"add_node": true}, // known, but graph never dispatches
			wantStatus:     http.StatusOK,
			wantActions:    []string{},
			wantOps:        []string{"add_node"},
			wantDispatched: []string{},
		},
		{
			name:       "unknown capability is 400",
			capability: "bogus",
			completion: completion(""),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp(t)
			var dispatched []string
			mounted.State.complete = func(context.Context, map[string]string, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
				return tc.completion, nil
			}
			mounted.State.known = func(_ context.Context, _ tools.Scope, name string) bool { return tc.known[name] }
			mounted.State.dispatch = func(_ context.Context, _ tools.Principal, name string, _ map[string]any) (any, error) {
				dispatched = append(dispatched, name)
				return map[string]any{"ok": true, "tool": name}, nil
			}

			status, out := req(t, app, "acme", map[string]any{
				"capability": tc.capability,
				"messages":   []message{{Role: "user", Content: "do the thing"}},
			})
			if status != tc.wantStatus {
				t.Fatalf("status: want %d, got %d", tc.wantStatus, status)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if out.Reply != tc.wantReply {
				t.Fatalf("reply: want %q, got %q", tc.wantReply, out.Reply)
			}
			gotActions := names(out.Actions, func(a action) string { return a.Name })
			if !eq(gotActions, tc.wantActions) {
				t.Fatalf("actions: want %v, got %v", tc.wantActions, gotActions)
			}
			gotOps := names(out.Ops, func(o op) string { return o.Name })
			if !eq(gotOps, tc.wantOps) {
				t.Fatalf("ops: want %v, got %v", tc.wantOps, gotOps)
			}
			sort.Strings(dispatched)
			if dispatched == nil {
				dispatched = []string{}
			}
			if !eq(dispatched, tc.wantDispatched) {
				t.Fatalf("dispatched: want %v, got %v", tc.wantDispatched, dispatched)
			}
			// A dispatched action carries the registry result.
			for _, a := range out.Actions {
				if a.Error != "" || a.Result == nil {
					t.Fatalf("action %s should carry a result, got %+v", a.Name, a)
				}
			}
		})
	}
}

// TestChatRequiresPrincipal: a request with no validated identity is refused 403 —
// a forged X-Org-Id with no credential never runs a round.
func TestChatRequiresPrincipal(t *testing.T) {
	app := newApp(t)
	mounted.State.complete = func(context.Context, map[string]string, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
		t.Fatal("completion must not run without a principal")
		return openai.ChatCompletionResponse{}, nil
	}
	status, _ := req(t, app, "", map[string]any{
		"capability": "graph",
		"messages":   []message{{Role: "user", Content: "hi"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("no principal: want 403, got %d", status)
	}
}

// TestChatRequiresMessages: an empty messages array is 400 before any completion.
func TestChatRequiresMessages(t *testing.T) {
	app := newApp(t)
	mounted.State.complete = func(context.Context, map[string]string, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
		t.Fatal("completion must not run with no messages")
		return openai.ChatCompletionResponse{}, nil
	}
	status, _ := req(t, app, "acme", map[string]any{"capability": "graph", "messages": []message{}})
	if status != http.StatusBadRequest {
		t.Fatalf("empty messages: want 400, got %d", status)
	}
}
