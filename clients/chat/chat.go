// Package chat is the ONE canonical chat orchestrator for Hanzo Cloud: POST
// /v1/chat runs a single LLM tool-calling round that lets a model manage a system
// through tools. It COMPOSES existing cloud pieces rather than reinventing them —
//
//   - LLM routing + per-org reserve/settle billing: the ai subsystem's
//     /v1/chat/completions, invoked in-process (the ONLY path that both returns
//     tool_calls and carries the billing gate);
//   - the unified tool plane (clients/tools): the org's registered MCP/registry
//     tools are offered to the model and dispatched server-side through the ONE
//     per-principal plane (activation + price gates included);
//   - capabilities (capability.go): the system prompt + builtin tool defs that
//     frame a round for a use case (graph editing, product/fashion render).
//
// A round yields three things: the assistant's text (reply), the tool calls the
// server executed against the registry (actions), and the tool calls the client
// must apply itself — a graph/UI mutation the server cannot run (ops).
//
// Route ownership: chat OWNS /v1/chat. The ai module also registers a beego
// /v1/chat alias behind its /v1/* catch-all, but chat mounts BEFORE ai in Wire, so
// Fiber's first-match resolves /v1/chat here and the alias is shadowed (never
// reached). ai keeps /v1/chat/completions and /v1/completions.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/tools"
	openai "github.com/sashabaranov/go-openai"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// maxCompletionResponse bounds the in-process completion body read so a hostile or
// broken upstream cannot balloon memory.
const maxCompletionResponse = 8 << 20

// completeFn runs one chat completion (with tools) and returns the parsed response.
// It is the seam onto the ai subsystem's /v1/chat/completions: the real one replays
// the request in-process carrying the caller's credential (so ai's per-org billing
// runs); tests inject a fake so the round is exercised without a live model.
type completeFn func(ctx context.Context, cred map[string]string, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)

// state is the chat subsystem's own data. The three registry seams default to the
// process-wide tool plane (tools.Default) and are overridable in tests, mirroring
// the guide subsystem's invoke/toolOK seams.
type state struct {
	model    string
	complete completeFn
	known    func(ctx context.Context, scope tools.Scope, name string) bool
	list     func(ctx context.Context, scope tools.Scope) []tools.Tool
	dispatch func(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error)
}

// mounted is the active service, retained so tests can inject the seams above.
var mounted *cloud.Service[state]

// Mount wires POST /v1/chat onto app. It composes the ai completion path and the
// tool plane; it owns no store, so there is no Shutdown.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("chat.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("chat.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "chat"), State: state{
		model:    strings.TrimSpace(deps.AIDefaultModel),
		complete: appCompleter(app),
		known: func(ctx context.Context, scope tools.Scope, name string) bool {
			return tools.Default().Exists(ctx, scope, name)
		},
		list: func(ctx context.Context, scope tools.Scope) []tools.Tool {
			return tools.Default().List(ctx, scope)
		},
		dispatch: func(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error) {
			return tools.Default().Dispatch(ctx, p, name, args)
		},
	}}
	mounted = s
	app.Post("/v1/chat", cloud.Handle(s, handleChat))
	s.Log.Info("chat mounted", "route", "/v1/chat", "capabilities", len(capabilities), "brand", deps.Brand)
	return nil
}

// ── request / response ──────────────────────────────────────────────────────────

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages   []message       `json:"messages"`
	Capability string          `json:"capability"`
	Tools      []openai.Tool   `json:"tools"`
	System     string          `json:"system"`
	State      json.RawMessage `json:"state"`
}

// action is one tool call the server executed against the registry.
type action struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Result any            `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// op is one tool call the client must apply itself (a graph/UI mutation).
type op struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type chatResponse struct {
	Reply   string   `json:"reply"`
	Actions []action `json:"actions"`
	Ops     []op     `json:"ops"`
}

// ── handler ─────────────────────────────────────────────────────────────────────

// handleChat runs one tool-calling round. It resolves the caller, assembles the
// tool set (capability builtins + caller tools + the org's registry tools), calls
// the ai completion in-process, then splits the model's tool calls: a call the
// registry knows (under a server-executed capability) is dispatched server-side and
// reported as an action; every other call is returned as an op for the client.
func handleChat(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := tools.PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body chatRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Messages) == 0 {
		return zip.ErrBadRequest("messages required")
	}
	mode, ok := capabilityFor(body.Capability)
	if !ok {
		return zip.ErrBadRequest("unknown capability: " + strings.TrimSpace(body.Capability))
	}

	scope := tools.Scope{Org: p.Org, Project: p.Project}
	req := openai.ChatCompletionRequest{
		Model:    s.State.model,
		Messages: assembleMessages(mode, body),
		Tools:    assembleTools(c.Context(), s, mode, body.Tools, scope),
	}

	resp, err := s.State.complete(c.Context(), credential(c), req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "chat: completion: %v", err)
	}
	if len(resp.Choices) == 0 {
		return zip.Errorf(http.StatusBadGateway, "chat: completion returned no choices")
	}
	msg := resp.Choices[0].Message

	out := chatResponse{Reply: msg.Content, Actions: []action{}, Ops: []op{}}
	for _, tc := range msg.ToolCalls {
		name := tc.Function.Name
		args := decodeArgs(tc.Function.Arguments)
		if mode.ServerExecuted && s.State.known(c.Context(), scope, name) {
			a := action{Name: name, Args: args}
			if result, derr := s.State.dispatch(c.Context(), p, name, args); derr != nil {
				a.Error = derr.Error()
			} else {
				a.Result = result
			}
			out.Actions = append(out.Actions, a)
			continue
		}
		out.Ops = append(out.Ops, op{Name: name, Args: args})
	}
	return c.JSON(http.StatusOK, out)
}

// ── assembly ────────────────────────────────────────────────────────────────────

// assembleMessages prepends the round's system prompt (the capability's, plus any
// caller-supplied system text) to the caller's messages.
func assembleMessages(mode capability, body chatRequest) []openai.ChatCompletionMessage {
	system := mode.SystemPrompt
	if extra := strings.TrimSpace(body.System); extra != "" {
		system = system + "\n\n" + extra
	}
	msgs := make([]openai.ChatCompletionMessage, 0, len(body.Messages)+1)
	if system != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: system})
	}
	for _, m := range body.Messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = openai.ChatMessageRoleUser
		}
		msgs = append(msgs, openai.ChatCompletionMessage{Role: role, Content: m.Content})
	}
	return msgs
}

// assembleTools builds the tool array offered to the model: the capability's
// builtin tools, then the caller-passed tool defs, then the org's ACTIVATED
// registry tools — deduped by function name (an earlier, more-specific def wins a
// name collision). Only activated + dispatchable registry tools are offered, the
// same agent-facing projection the unified MCP endpoint uses (tools/http.go), so a
// chat sees exactly the tools the org turned on (its registered MCP servers for the
// create capability) rather than the whole route table.
func assembleTools(ctx context.Context, s *cloud.Service[state], mode capability, caller []openai.Tool, scope tools.Scope) []openai.Tool {
	out := make([]openai.Tool, 0, len(mode.BuiltinTools)+len(caller))
	seen := map[string]bool{}
	add := func(t openai.Tool) {
		if t.Function == nil || t.Function.Name == "" || seen[t.Function.Name] {
			return
		}
		seen[t.Function.Name] = true
		out = append(out, t)
	}
	for _, t := range mode.BuiltinTools {
		add(t)
	}
	for _, t := range caller {
		add(t)
	}
	for _, t := range s.State.list(ctx, scope) {
		if !t.Activated || !t.Dispatchable || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schemaOrEmpty(t.Schema),
			},
		})
	}
	return out
}

// schemaOrEmpty returns the tool's JSON-Schema, or the empty-object schema when a
// source published none (the model requires a parameters object).
func schemaOrEmpty(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return schema
}

// decodeArgs parses a tool call's JSON argument string into a map. A model may emit
// empty or malformed arguments; that yields an empty map rather than an error, so a
// no-arg tool call still dispatches / returns as an op.
func decodeArgs(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return map[string]any{}
	}
	return args
}

// ── in-process completion ───────────────────────────────────────────────────────

// credentialHeaders are the caller's own credential headers replayed on the
// in-process completion, so cloud's SanitizeIdentity re-derives the SAME validated
// identity for the ai handler. No minted authority header (X-Org-Id/…) is replayed —
// those are stripped on ingress and re-minted from the credential. This mirrors the
// tool plane's builtin-replay contract (clients/tools).
var credentialHeaders = []string{"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For"}

func credential(c *zip.Ctx) map[string]string {
	cred := map[string]string{}
	for _, h := range credentialHeaders {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	return cred
}

// appCompleter is the real completeFn: it replays the request against the SAME
// Fiber app in-process, so it flows through the whole middleware chain and lands on
// the ai subsystem's /v1/chat/completions — the one path that returns tool_calls
// AND carries per-org reserve/settle billing. Non-streaming, so the upstream
// forwards a single JSON ChatCompletionResponse. This is the tool plane's in-process
// dispatch pattern (clients/tools/builtin.go) applied to the LLM round.
func appCompleter(app *zip.App) completeFn {
	return func(ctx context.Context, cred map[string]string, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
		req.Stream = false
		b, err := json.Marshal(req)
		if err != nil {
			return openai.ChatCompletionResponse{}, err
		}
		hreq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)).WithContext(ctx)
		hreq.Header.Set("Content-Type", "application/json")
		for k, v := range cred {
			hreq.Header.Set(k, v)
		}
		resp, err := app.Fiber().Test(hreq, fiber.TestConfig{Timeout: 0})
		if err != nil {
			return openai.ChatCompletionResponse{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxCompletionResponse))
		if resp.StatusCode/100 != 2 {
			return openai.ChatCompletionResponse{}, fmt.Errorf("upstream status %d: %s", resp.StatusCode, clip(raw))
		}
		var out openai.ChatCompletionResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return openai.ChatCompletionResponse{}, fmt.Errorf("decode completion: %w", err)
		}
		return out, nil
	}
}

func clip(b []byte) string {
	const max = 256
	if len(b) > max {
		return string(b[:max])
	}
	return string(b)
}
