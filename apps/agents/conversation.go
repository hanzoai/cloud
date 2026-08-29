// The conversation surface: a conversation that uses your org's own tools to get
// an answer.
//
// It mounts the hanzoai/agent orchestrator into cloud: POST /v1/agents/chat (+
// /v1/agents/chat/presets, /v1/agents/chat/conversations). The orchestrator logic
// and its per-org conversation history live in github.com/hanzoai/agent, which
// imports NEITHER cloud NOR ai. Cloud is the composition root: it injects the two
// clients —
//   - Completer: the ai subsystem's /v1/chat/completions, replayed in-process (the
//     one path that returns tool_calls AND carries per-org reserve/settle billing);
//   - ToolPlane: the unified tool registry (tools.Default()), so the round's
//     server-executed tools are the org's activated MCP/registry tools.
//
// The round answers UNDER the agents root rather than at it, because POST
// /v1/agents is already the typed create. Which address it takes is cloud's to
// decide: hz.MountAt registers the five routes wherever the composer points them
// (HIP-1210), so the surface no longer needs a root of its own.
package agents

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	openai "github.com/hanzoai/go-openai"
	"github.com/zap-proto/zip"
)

// maxCompletionResponse bounds the in-process completion body read so a hostile or
// broken upstream cannot balloon memory.
const maxCompletionResponse = 8 << 20

// chat is where the round answers. The agents root is spent — POST /v1/agents is
// the typed create — so the conversation surface takes a sub-path of it, and the
// five routes compose off this one address (HIP-1210).
const chat = "/v1/agents/chat"

// UNTYPED BY DESIGN, and not fixable here. All five operations — POST /v1/agents/chat,
// GET /v1/agents/chat/presets, POST and GET /v1/agents/chat/conversations,
// GET /v1/agents/chat/conversations/{id} — are registered by hz.MountAt below, which
// is github.com/hanzoai/agent's own router wiring (agent.go in v1.0.7). This
// package registers NO route of its own, so there is nothing in cloud to convert:
// they become typeable in hanzoai/agent, which owns them, exactly as apps/tasks'
// relayed operations become typeable in hanzoai/tasks.
//
// Two facts have to move upstream with them, and both are visible from here:
//
//   - Every operation resolves its caller through a `func(*zip.Ctx) (Principal,
//     bool)` (Deps.Principal, supplied below), and the round additionally
//     dispatches server-executed tools with the LIVE *zip.Ctx (orchestratorTools.Dispatch)
//     and replays the caller's own credential HEADERS into the in-process
//     completion (credential, below). A typed op receives only a context, and
//     hanzoai/agent deliberately imports neither cloud nor ai, so it cannot use
//     cloud.Bridge — it needs a per-request client of its own before any of its five
//     handlers can lose its *zip.Ctx. The address moved and that client did not: it
//     is a per-REQUEST hole, indifferent to which path the request arrived on.
//   - The round passes an upstream 4xx through VERBATIM — the completion's own
//     status AND body, so a 402 insufficient_balance reaches the caller as itself
//     rather than as a gateway 502 (round.go:104-110 upstream). A typed op's only
//     way to answer non-2xx is to return an error, which zip renders as its flat
//     {status,code,error}; that route is the apps/ml refusal class and stays
//     untyped even after the client lands.
//
// Until then these five remain untyped, and so carry no MCP tool, no CLI command
// and no typed SDK method. What they DO carry is prose: openapi.Describe below
// declares it beside the wire fact, which is the client for exactly the operation a
// typed op cannot lift a doc comment into. Describe is additive metadata keyed on
// (method, path) and renders only while the router actually serves the route, so
// declaring the prose here — for routes hz.Use registers — cannot invent an
// operation, and it does not wait on the upstream work above.
func init() {
	openapi.Describe(chat, http.MethodPost,
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
	openapi.Describe(chat+"/presets", http.MethodGet,
		"List the agent presets available to a caller",
		"Returns the preset catalog: each entry's id, its description and whether it is "+
			"server-executing — the flag that decides if a preset's tool calls run here or "+
			"come back for the client to apply. The ids are what the round accepts in "+
			"`preset`.\n\n"+
			"The catalog is compiled into the build, identical for every caller, and this is "+
			"the one read in the group that needs no principal.")
	openapi.Describe(chat+"/conversations", http.MethodPost,
		"Record turns in a conversation",
		"Writes turns to the caller's thread store without running a completion, and answers "+
			"the `conversationId` they were written under. An absent `conversationId` opens a "+
			"new thread; supplying one appends to it.\n\n"+
			"This is for a client that streams its own turn through /v1/chat/completions and "+
			"still wants the conversation in its history — the round records what IT answers, "+
			"and is otherwise the only writer. It takes the same store, the same per-org "+
			"isolation and the same notion of a thread: what is recorded here reads back "+
			"through the two GETs beside it and the round can continue it by id. A validated "+
			"principal with a non-empty org is required; 403 without one.")
	openapi.Describe(chat+"/conversations", http.MethodGet,
		"List the agent threads in your org",
		"Returns a summary of every agent conversation in the caller's org — id, derived "+
			"title, and when it was last appended to — for populating a thread list.\n\n"+
			"Scoped to the caller's org and nothing else, and that isolation is structural "+
			"rather than a filter: conversations are persisted in a store opened PER ORG, so "+
			"there is no query in which another tenant's threads could appear. A validated "+
			"principal with a non-empty org is required; 403 without one.")
	openapi.Describe(chat+"/conversations/:id", http.MethodGet,
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

// mountConversation registers the tool-using conversation surface (the round and
// its presets/conversations) inside this app, injecting the ai completion and the
// tool plane; the caller identity comes from cloud's validated principal. It was a
// SECOND app named `agent` beside this one named `agents` — one concept, two
// plugins, and a pair of names a reader could not tell apart — and then a second
// ROOT after the apps merged. Both names are now one.
func mountConversation(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agent.Use:  nil app")
	}
	// hanzoai/agent registers TYPED ops, and the op registry lives on the concrete
	// App — so this is the named hole (cloud.ZipApp), not a widened parameter. The
	// signature stays the fleet's one UseFunc, and agent installs no app-wide
	// middleware (hanzoai/agent calls Use nowhere), so it mounts SCOPED: taking the
	// concrete type used to cost it the whole binary's middleware grant.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("agent.Use:  router is not a zip app — the typed op registry is unreachable")
	}
	_, err := hz.MountAt(zapp, chat, hz.Deps{
		DataDir: deps.DataDir,
		Brand:   deps.Brand,
		Model:   cloud.DefaultModel,
		Principal: func(c *zip.Ctx) (hz.Principal, bool) {
			p, ok := tools.PrincipalFrom(c)
			if !ok {
				return hz.Principal{}, false
			}
			return hz.Principal{Org: p.Org, Project: p.Project, User: p.User, Cred: credential(c)}, true
		},
	}, aiCompleter{ai: deps.AI}, orchestratorTools{})
	return err
}

// ── Completer: the AI subsystem, through the one client that reaches it ───────────

type aiCompleter struct{ ai types.AIClient }

// Complete asks the AI subsystem for one tool-calling completion.
//
// IT GOES THROUGH deps.AI, which is how every other app on this estate reaches a
// completion — translate, projects, the lot. This used to replay the request
// against its OWN router with fiber's Test hook, on the reasoning that
// /v1/chat/completions is "the SAME app". It is the same app only where every
// subsystem is linked into one binary. Where they are separate plugin processes
// the ai routes are not in the agents process's router at all, so the replay
// matched nothing and returned fiber's bare 404 — which the round then passed
// through verbatim as a caller-facing "not found", after it had already
// persisted the user's turn. Every conversation held a question and no answer.
//
// The billing scope travels as DATA rather than as a replayed Authorization
// header: Org is whose data this is and BillingOrg is who pays, which is the
// same split types.ChatRequest already documents and the same one a SuperAdmin
// acting in another org depends on.
func (a aiCompleter) Complete(ctx context.Context, cred map[string]string, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if a.ai == nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("agent: no AI client")
	}
	org := strings.TrimSpace(cred[scopeOrg])
	if org == "" {
		// A caller that reached this point had an org — the round refuses one
		// without. An empty value here means the scope did not travel, and a
		// completion billed to nobody is worse than one that does not happen.
		if acting, err := principal.Acting(ctx); err == nil {
			org = strings.TrimSpace(acting)
		}
	}
	if org == "" {
		// The round refuses a caller without an org before it ever gets here, so
		// an empty one means the identity did not survive into this context —
		// which would bill the completion to nobody. Fail rather than guess.
		return openai.ChatCompletionResponse{}, fmt.Errorf("agent: no org on the call")
	}
	in := &types.ChatRequest{
		Model:      req.Model,
		Org:        org,
		BillingOrg: cmp.Or(strings.TrimSpace(cred[scopeOwner]), org),
		Project:    cloud.Who(ctx).Project,
		MaxTokens:  req.MaxTokens,
		Messages:   inMessages(req.Messages),
		Tools:      inTools(req.Tools),
	}

	out, err := a.ai.ChatCompletion(ctx, in)
	if err != nil {
		// A completion refused for the caller's OWN reason (402 insufficient_balance,
		// 429, 403) is the caller's error and travels as itself; the client wraps
		// the upstream error, so the status is still reachable through it.
		if status, ok := upstreamStatus(err); ok && status >= 400 && status < 500 {
			return openai.ChatCompletionResponse{}, &hz.UpstreamError{
				Status: status,
				Body:   []byte(fmt.Sprintf(`{"error":{"message":%q}}`, err.Error())),
			}
		}
		return openai.ChatCompletionResponse{}, err
	}

	return openai.ChatCompletionResponse{
		Model: req.Model,
		Choices: []openai.ChatCompletionChoice{{
			Message:      outMessage(out),
			FinishReason: openai.FinishReason(out.FinishReason),
		}},
		Usage: openai.Usage{
			PromptTokens:     out.PromptTokens,
			CompletionTokens: out.CompletionTokens,
			TotalTokens:      out.TotalTokens,
		},
	}, nil
}

// upstreamStatus recovers the status a refusal should reach the caller as,
// through however many layers wrapped it.
//
// A REFUSAL IS THE CALLER'S, NOT A FAULT. An unfunded org asking for a
// completion is answered 402 and told to add credit; wrapped as a 502 it reads
// as "the gateway is broken", which sends somebody debugging the wrong thing.
// The meter refuses before the request ever leaves, so its error carries no HTTP
// status of its own and has to be named here.
func upstreamStatus(err error) (int, bool) {
	// A refusal that crossed a process boundary arrives as an *HTTPError carrying
	// the number the callee chose — the crossing preserves the status precisely
	// so it does not flatten. It does NOT preserve the sentinel's identity, so
	// this is checked first: on this estate the meter runs in the subsystem being
	// asked, and by the time its answer is back the error is a status and a
	// sentence, not the value errors.Is could recognise.
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		return he.Status, true
	}
	// In-process, where the sentinel is still itself.
	if errors.Is(err, metering.ErrInsufficientBalance) || errors.Is(err, metering.ErrSpendCapExceeded) {
		return http.StatusPaymentRequired, true
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode > 0 {
		return apiErr.HTTPStatusCode, true
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) && reqErr.HTTPStatusCode > 0 {
		return reqErr.HTTPStatusCode, true
	}
	return 0, false
}

// inMessages carries the transcript across, tool calls included. The ID linking
// a call to its result IS the loop — drop it and the model is guessing which
// answer belongs to which question.
func inMessages(in []openai.ChatCompletionMessage) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(in))
	for _, m := range in {
		msg := types.ChatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				continue
			}
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		out = append(out, msg)
	}
	return out
}

// inTools carries what the model may call. A tool with no name is not a tool.
func inTools(in []openai.Tool) []types.ToolDef {
	out := make([]types.ToolDef, 0, len(in))
	for _, t := range in {
		if t.Function == nil || t.Function.Name == "" {
			continue
		}
		schema, err := json.Marshal(t.Function.Parameters)
		if err != nil {
			continue
		}
		out = append(out, types.ToolDef{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Schema:      schema,
		})
	}
	return out
}

// outMessage is the assistant turn the round reads: its words, and the calls it
// wants run before it can finish.
func outMessage(out *types.ChatResponse) openai.ChatCompletionMessage {
	msg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: out.Content,
	}
	for _, tc := range out.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
			ID:       tc.ID,
			Type:     openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return msg
}

// ── ToolPlane: adapter over the unified registry ──────────────────────────────────

type orchestratorTools struct{}

func (orchestratorTools) List(ctx context.Context, scope hz.Scope) []hz.Tool {
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

func (orchestratorTools) Exists(ctx context.Context, scope hz.Scope, name string) bool {
	return tools.Default().Exists(ctx, tools.Scope{Org: scope.Org, Project: scope.Project}, name)
}

// Dispatch resolves the caller the ONE canonical way — tools.PrincipalFrom(c) — so
// the tool runs under the SAME validated identity + credential as a direct call.
// No reconstruction: the credential is only ever read from the live request.
func (orchestratorTools) Dispatch(c *zip.Ctx, name string, args map[string]any) (any, error) {
	p, ok := tools.PrincipalFrom(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	return tools.Default().Dispatch(c.Context(), p, name, args)
}

// ── helpers ───────────────────────────────────────────────────────────────────────

// scopeOrg and scopeOwner are where the caller's BILLING SCOPE rides to the
// completer.
//
// The tool plane's Dispatch is handed the live *zip.Ctx and reads the principal
// straight off it; Complete is handed a bare context and cannot, and the context
// an HTTP request arrives on carries no org — so the completion had no idea who
// to bill and refused itself. The map the interface DOES pass through is this
// one, so the scope travels in it.
//
// The dot makes each key an illegal HTTP header name on purpose: this map is
// otherwise a set of headers to replay, and a value that can never be mistaken
// for one can never be sent as one.
const (
	scopeOrg   = "hanzo.org"
	scopeOwner = "hanzo.owner"
)

// credential extracts the caller's replayable credential headers (the same set the
// tool plane replays), and the billing scope the completer cannot otherwise see.
func credential(c *zip.Ctx) map[string]string {
	cred := map[string]string{}
	for _, h := range []string{"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For"} {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	if p, ok := tools.PrincipalFrom(c); ok {
		cred[scopeOrg] = p.Org
		cred[scopeOwner] = cloud.Who(c.Context()).Owner
	}
	return cred
}
