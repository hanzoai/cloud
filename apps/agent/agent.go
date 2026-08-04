// Package agent is a conversation that uses your org's own tools to get an answer.
//
// It mounts the hanzoai/agent orchestrator into cloud: POST /v1/agent (+
// /v1/agent/presets, /v1/agent/conversations). The orchestrator logic and its
// per-org conversation history live in github.com/hanzoai/agent, which imports
// NEITHER cloud NOR ai. Cloud is the composition root: it injects the two seams
// —
//   - Completer: the ai subsystem's /v1/chat/completions, replayed in-process (the
//     one path that returns tool_calls AND carries per-org reserve/settle billing);
//   - ToolPlane: the unified tool registry (tools.Default()), so /v1/agent's
//     server-executed tools are the org's activated MCP/registry tools.
//
// /v1/agent is a DISTINCT path (not /v1/chat, which ai owns as completions), so a
// specific route wins over ai's /v1/* glob — no collision.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	fiber "github.com/zap-proto/fiber/v3"
	"io"
	"net/http"
	"net/http/httptest"

	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/openapi"
	openai "github.com/hanzoai/go-openai"
	"github.com/zap-proto/zip"
)

// maxCompletionResponse bounds the in-process completion body read so a hostile or
// broken upstream cannot balloon memory.
const maxCompletionResponse = 8 << 20

// UNTYPED BY DESIGN, and not fixable here. All four /v1/agent operations —
// POST /v1/agent, GET /v1/agent/presets, GET /v1/agent/conversations,
// GET /v1/agent/conversations/{id} — are registered by hz.Mount below, which is
// github.com/hanzoai/agent's own router wiring (agent.go:166-169 in v0.1.3). This
// package registers NO route of its own, so there is nothing in cloud to convert:
// they become typeable in hanzoai/agent, which owns them, exactly as apps/tasks'
// relayed operations become typeable in hanzoai/tasks.
//
// Two facts have to move upstream with them, and both are visible from here:
//
//   - Every operation resolves its caller through a `func(*zip.Ctx) (Principal,
//     bool)` (Deps.Principal, supplied below), and POST /v1/agent additionally
//     dispatches server-executed tools with the LIVE *zip.Ctx (toolPlane.Dispatch)
//     and replays the caller's own credential HEADERS into the in-process
//     completion (credential, below). A typed op receives only a context, and
//     hanzoai/agent deliberately imports neither cloud nor ai, so it cannot use
//     cloud.Bridge — it needs a per-request seam of its own before any of its four
//     handlers can lose its *zip.Ctx.
//   - POST /v1/agent passes an upstream 4xx through VERBATIM — the completion's own
//     status AND body, so a 402 insufficient_balance reaches the caller as itself
//     rather than as a gateway 502 (round.go:104-110 upstream). A typed op's only
//     way to answer non-2xx is to return an error, which zip renders as its flat
//     {status,code,error}; that route is the apps/ml refusal class and stays
//     untyped even after the seam lands.
//
// Until then these four remain untyped, and so carry no MCP tool, no CLI command
// and no typed SDK method. What they DO carry is prose: openapi.Describe below
// declares it beside the wire fact, which is the seam for exactly the operation a
// typed op cannot lift a doc comment into. Describe is additive metadata keyed on
// (method, path) and renders only while the router actually serves the route, so
// declaring the prose here — for routes hz.Mount registers — cannot invent an
// operation, and it does not wait on the upstream work above.
func init() {
	openapi.Describe("/v1/agent", http.MethodPost,
		"Run one tool-calling round against your org's own tools",
		"Answers one turn of a conversation with four things: the model's `reply`, the "+
			"`actions` the server executed on the caller's behalf, the `ops` the client must "+
			"apply itself, and the `conversationId` the turn was recorded under.\n\n"+
			"The split between actions and ops is the rule most easily got wrong. A tool call "+
			"is executed HERE only when the chosen preset is server-executing AND the tool "+
			"resolves in the caller's own scope; every other call is handed back as an op for "+
			"the client to apply to its own graph or UI. A tool that fails still comes back as "+
			"an action, carrying its error rather than failing the round.\n\n"+
			"`preset` selects the system prompt and the tool set (`capability` is a legacy "+
			"alias for it); an unknown one is refused. `conversationId` continues an existing "+
			"thread, and its absence starts one. A validated principal with a non-empty org is "+
			"required — the org is the sole authority for both persistence and tool scope, and "+
			"is NEVER read from the body.\n\n"+
			"A completion refused for the caller's own reason — 402 insufficient balance, 429, "+
			"403 — is relayed with its own status and body verbatim, so the real billing "+
			"message reaches the client instead of an opaque gateway error. Only a genuine "+
			"upstream fault becomes a 502.")
	openapi.Describe("/v1/agent/presets", http.MethodGet,
		"List the agent presets available to a caller",
		"Returns the preset catalog: each entry's id, its description and whether it is "+
			"server-executing — the flag that decides if a preset's tool calls run here or "+
			"come back for the client to apply. The ids are what POST /v1/agent accepts in "+
			"`preset`.\n\n"+
			"The catalog is compiled into the build, identical for every caller, and this is "+
			"the one read in the group that needs no principal.")
	openapi.Describe("/v1/agent/conversations", http.MethodGet,
		"List the agent threads in your org",
		"Returns a summary of every agent conversation in the caller's org — id, derived "+
			"title, and when it was last appended to — for populating a thread list.\n\n"+
			"Scoped to the caller's org and nothing else, and that isolation is structural "+
			"rather than a filter: conversations are persisted in a store opened PER ORG, so "+
			"there is no query in which another tenant's threads could appear. A validated "+
			"principal with a non-empty org is required; 403 without one.")
	openapi.Describe("/v1/agent/conversations/:id", http.MethodGet,
		"Read one agent thread in full",
		"Returns every message of one conversation in order — role, content, the assistant's "+
			"tool calls where it made any, and each message's creation time — which is the "+
			"transcript a client replays to resume a thread.\n\n"+
			"The lookup happens inside the caller's OWN per-org store, so an id belonging to "+
			"another tenant is not refused, it is simply absent: the answer is 200 with an "+
			"empty message list. Read it as \"no such conversation for you\" rather than as an "+
			"empty thread. A validated principal with a non-empty org is required; 403 without "+
			"one.")
}

// Mount wires POST /v1/agent (+ reads) into cloud, injecting the ai completion and
// the tool plane. The caller identity comes from cloud's validated principal.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agent.Mount: nil app")
	}
	_, err := hz.Mount(app, hz.Deps{
		Logger:  deps.Logger,
		DataDir: deps.DataDir,
		Brand:   deps.Brand,
		Model:   deps.AIDefaultModel,
		Principal: func(c *zip.Ctx) (hz.Principal, bool) {
			p, ok := tools.PrincipalFrom(c)
			if !ok {
				return hz.Principal{}, false
			}
			return hz.Principal{Org: p.Org, Project: p.Project, User: p.User, Cred: credential(c)}, true
		},
	}, aiCompleter{app: app}, toolPlane{})
	return err
}

// ── Completer: replay /v1/chat/completions in-process ─────────────────────────────

type aiCompleter struct{ app cloud.Router }

// Complete replays the request against the SAME app at /v1/chat/completions, so it
// flows the whole middleware chain (per-org reserve/settle billing) and returns
// tool_calls. Non-streaming. Mirrors the tool plane's in-process dispatch contract:
// the caller's OWN credential headers are replayed; no minted authority header.
func (a aiCompleter) Complete(ctx context.Context, cred map[string]string, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
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
	resp, err := a.app.Fiber().Test(hreq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxCompletionResponse))
	if resp.StatusCode/100 != 2 {
		// Carry the completion's OWN status + body so the round can pass a
		// caller-facing refusal (402 insufficient_balance, 429, 403) straight
		// through instead of masking it as a gateway 502. hz.UpstreamError is the
		// agent's typed seam for exactly this.
		return openai.ChatCompletionResponse{}, &hz.UpstreamError{Status: resp.StatusCode, Body: raw}
	}
	var out openai.ChatCompletionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("decode completion: %w", err)
	}
	return out, nil
}

// ── ToolPlane: adapter over the unified registry ──────────────────────────────────

type toolPlane struct{}

func (toolPlane) List(ctx context.Context, scope hz.Scope) []hz.Tool {
	ts := tools.Default().List(ctx, tools.Scope{Org: scope.Org, Project: scope.Project})
	out := make([]hz.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, hz.Tool{
			Name:         t.Name,
			Description:  t.Description,
			Schema:       t.Schema,
			Activated:    t.Activated,
			Dispatchable: t.Dispatchable,
		})
	}
	return out
}

func (toolPlane) Exists(ctx context.Context, scope hz.Scope, name string) bool {
	return tools.Default().Exists(ctx, tools.Scope{Org: scope.Org, Project: scope.Project}, name)
}

// Dispatch resolves the caller the ONE canonical way — tools.PrincipalFrom(c) — so
// the tool runs under the SAME validated identity + credential as a direct call.
// No reconstruction: the credential is only ever read from the live request.
func (toolPlane) Dispatch(c *zip.Ctx, name string, args map[string]any) (any, error) {
	p, ok := tools.PrincipalFrom(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	return tools.Default().Dispatch(c.Context(), p, name, args)
}

// ── helpers ───────────────────────────────────────────────────────────────────────

// credential extracts the caller's replayable credential headers (the same set the
// tool plane replays) so the in-process completion runs as the caller.
func credential(c *zip.Ctx) map[string]string {
	cred := map[string]string{}
	for _, h := range []string{"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For"} {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	return cred
}
