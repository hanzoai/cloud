// The conversation surface: a conversation that uses your org's own tools to get
// an answer.
//
// It mounts the hanzoai/agent orchestrator into cloud: POST /v1/agent/chat (+
// /v1/agent/chat/presets, /v1/agent/chat/conversations). The orchestrator logic
// and its per-org conversation history live in github.com/hanzoai/agent, which
// imports NEITHER cloud NOR ai. Cloud is the composition root: it injects the two
// clients —
//   - Completer: the ai subsystem's /v1/chat/completions, replayed in-process (the
//     one path that returns tool_calls AND carries per-org reserve/settle billing);
//   - ToolPlane: the unified tool registry (tools.Default()), so the round's
//     server-executed tools are the org's activated MCP/registry tools.
//
// The round answers UNDER the agents root rather than at it, because POST
// /v1/agent is already the typed create. Which address it takes is cloud's to
// decide: hz.MountAt registers the five routes wherever the composer points them
// (HIP-1210), so the surface no longer needs a root of its own.

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/account"
	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/authz"
	openai "github.com/hanzoai/go-openai"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// chat is where the round answers. The agents root is spent — POST /v1/agent is
// the typed create — so the conversation surface takes a sub-path of it, and the
// five routes compose off this one address (HIP-1210).
const chat = "/v1/agent/chat"

// UNTYPED BY DESIGN, and not fixable here. All sixteen operations — POST
// /v1/agent/chat, GET /v1/agent/chat/presets, POST and GET
// /v1/agent/chat/conversations, GET, PATCH and DELETE
// /v1/agent/chat/conversations/{id}, POST and
// GET /v1/agent/chat/conversations/{id}/shares, DELETE
// /v1/agent/chat/conversations/{id}/shares/{share} and its
// /viewers/{viewer}, POST /v1/agent/chat/shares/read, GET
// /v1/agent/chat/shared and /shared/{share}, and GET and DELETE
// /v1/agent/chat/shares[/{share}] — are registered by hz.MountAt below, which
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
	spec.Describe(chat, http.MethodPost,
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
	spec.Describe(chat+"/presets", http.MethodGet,
		"List the agent presets available to a caller",
		"Returns the preset catalog: each entry's id, its description and whether it is "+
			"server-executing — the flag that decides if a preset's tool calls run here or "+
			"come back for the client to apply. The ids are what the round accepts in "+
			"`preset`.\n\n"+
			"The catalog is compiled into the build, identical for every caller, and this is "+
			"the one read in the group that needs no principal.")
	spec.Describe(chat+"/conversations", http.MethodPost,
		"Record turns in a conversation",
		"Writes turns to the caller's thread store without running a completion, and answers "+
			"the `conversationId` they were written under. An absent `conversationId` opens a "+
			"new thread; supplying one appends to it.\n\n"+
			"This is for a client that streams its own turn through /v1/chat/completions and "+
			"still wants the conversation in its history — the round records what IT answers, "+
			"and is otherwise the only writer. It takes the same store, the same per-org "+
			"isolation and the same notion of a thread: what is recorded here reads back "+
			"through the two GETs beside it and the round can continue it by id. A validated "+
			"principal with a non-empty org is required: 401 without a principal, 403 without an org.")
	spec.Describe(chat+"/conversations", http.MethodGet,
		"List your own conversations",
		"Returns a summary of each conversation the caller opened — id, title, when it was "+
			"last appended to, and whether it is pinned or archived — for populating a thread "+
			"list: pinned first, then most recent. Archived conversations are left out; "+
			"`?archived=true` lists them alone.\n\n"+
			"A conversation is the person's who opened it, not the org's: another member of "+
			"the same org lists none of it. Each org's conversations live in a store of its "+
			"own, and inside it only the caller's are listed. One recorded before members "+
			"were kept names nobody and is nobody's: `?memberless=true` lists those instead, "+
			"for a SuperAdmin alone (403 for anyone else), and each such list is audited. A "+
			"validated principal naming an org and a user the identity boundary attests no "+
			"two people share is required: 401 without a principal, 403 without an org or "+
			"such a user.")
	spec.Describe(chat+"/conversations/:id", http.MethodGet,
		"Read one agent thread in full",
		"Returns every message of one of the caller's conversations in order — role, content, "+
			"the assistant's tool calls where it made any, and each message's creation time — "+
			"which is the transcript a client replays to resume a thread.\n\n"+
			"Another member's conversation, another org's, and one recorded with no member are "+
			"answered 404, exactly as an id that never existed, so an id says nothing about "+
			"whose it is (a SuperAdmin alone reads one with no member, and each read is "+
			"audited). A turn sent under such "+
			"an id opens a fresh conversation of the caller's. A validated principal naming an "+
			"org and a user is required: 401 without a principal, 403 without an org or a user.")
	spec.Describe(chat+"/conversations/:id", http.MethodPatch,
		"Rename, pin or archive one of your conversations",
		"Takes `{title?, pinned?, archived?}` and changes what it names, leaving the rest; "+
			"a body naming none is refused 400, and so is an empty title. A title is cut to one "+
			"line of at most 80 characters. Answers the conversation as the list does — id, "+
			"title, updatedAt, pinned, archived — and the change does not move `updatedAt`, which "+
			"stays when it was last spoken in.\n\n"+
			"The list puts pinned conversations first and leaves archived ones out; "+
			"`?archived=true` lists those alone. Only the member who opened the conversation may "+
			"change it; another member's, another org's, and one recorded with no member answer "+
			"404. Every change is written to the audit trail.")
	spec.Describe(chat+"/conversations/:id", http.MethodDelete,
		"Delete one of your conversations",
		"Removes the conversation for good: every turn of it, and every link it was shared "+
			"by, which stops opening and leaves the lists of the people it was open to. Only "+
			"the member who opened it may delete it; anyone else is answered 404, as for one "+
			"that is not there. Every delete is written to the audit trail.")
	spec.Describe(chat+"/conversations/:id/shares", http.MethodPost,
		"Share one of your conversations by link",
		"Makes a read-only link to a conversation the caller owns and answers the share and "+
			"its `token`. The token is 256 random bits and is returned ONCE: the store keeps "+
			"only its SHA-256, so a lost token is replaced by making another share, never "+
			"read back. The token names the chat and authenticates nobody.\n\n"+
			"A share is a snapshot of the user and assistant turns up to the moment it was "+
			"made. System turns, tool calls and tool results are never part of it, and turns "+
			"added later need a new share. Only the member who opened the conversation may "+
			"share it; anyone else — and anyone at all for a conversation recorded with no "+
			"member — is answered 404. Every share made is written to the audit trail.")
	spec.Describe(chat+"/conversations/:id/shares", http.MethodGet,
		"List the live links to one of your conversations",
		"Returns each unrevoked share of a conversation the caller owns: its id, its access "+
			"(`read`), when it was made, and its `viewers` — each signed-in person who opened "+
			"it, with the name they signed in with and when. Tokens are not in the list; they "+
			"are returned only when a share is made.")
	spec.Describe(chat+"/conversations/:id/shares/:share", http.MethodDelete,
		"Revoke a link to one of your conversations",
		"Ends one share of a conversation the caller owns, for every viewer: it leaves "+
			"their lists, and its token opens nothing and reads exactly like a token that "+
			"never existed. Every revoke is written to the audit trail.")
	spec.Describe(chat+"/conversations/:id/shares/:share/viewers/:viewer", http.MethodDelete,
		"Remove one viewer from a link",
		"Closes one share of a conversation the caller owns to one viewer: it leaves their "+
			"list, and the token no longer opens it for them. Everyone else the share is open "+
			"to keeps it. Every removal is written to the audit trail.")
	spec.Describe(chat+"/shares/read", http.MethodPost,
		"Open a conversation shared by link",
		"Takes `{token}` in the body, so the token never sits in a URL a log keeps. A caller "+
			"who is not a signed-in person gets the title and `full: false`, and no turn of the "+
			"transcript — which is what a sign-in prompt shows. A signed-in person, of any org, "+
			"is recorded as a viewer the owner can see, and reads the whole snapshot with the "+
			"share's id; the reader's own org is neither read nor changed. An assistant turn "+
			"carries `model` only when this server stored it from that model's completion. A "+
			"token that does not open — malformed, unknown, revoked, or closed to this viewer — "+
			"is answered 404 with one sentence for all.")
	spec.Describe(chat+"/shared", http.MethodGet,
		"List the chats shared with you",
		"Returns each live share the caller opened signed in: its id, the conversation's "+
			"title and when the caller first opened it, most recent first. A revoked share, or "+
			"one its owner closed to the caller, is not in it. A signed-in person is required.")
	spec.Describe(chat+"/shared/:share", http.MethodGet,
		"Read a chat shared with you",
		"Returns the snapshot of a share the caller is a viewer of, without its token, in "+
			"the shape POST /shares/read answers a signed-in person. Anyone who is not one of "+
			"its viewers is answered 404.")
	spec.Describe(chat+"/shares", http.MethodGet,
		"List every live link in your organization",
		"For an admin of the caller's organization: every unrevoked share made in it, with "+
			"the conversation's id and title, the member who made it, when, and how many "+
			"people it is open to. `?user=` narrows it to one member's. Anyone else is "+
			"answered 403, and so is an admin of a held org such as hanzo, whose members are "+
			"customers who are strangers to each other.")
	spec.Describe(chat+"/shares/:share", http.MethodDelete,
		"Revoke any link in your organization",
		"For an admin of the caller's organization: ends one share made in it, for every "+
			"viewer, exactly as its owner's revoke does. Anyone else is answered 403 (an admin "+
			"of a held org included), and a share of another organization 404. Every revoke is "+
			"written to the audit trail.")
}

// chatDBs is the databases the conversation surface borrows (chat_store.go).
var chatDBs *chatStores

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
	if app == nil {
		return fmt.Errorf("agent.Use:  router is not a zip app — the typed op registry is unreachable")
	}
	stores := newChatStores(deps)
	chatDBs = stores
	_, err := hz.MountAt(app, chat, hz.Deps{
		DataDir: cloud.DataDir(),
		Open:    stores.open,
		Shares:  stores.index,
		Brand:   cloud.Brand(),
		Model:   aimodel.DefaultModel,
		Principal: func(c *zip.Ctx) (hz.Principal, bool) {
			p, ok := tools.PrincipalFrom(c)
			if !ok {
				return hz.Principal{}, false
			}
			m, _ := principal.Minted(c)
			// A conversation is keyed on the subject the boundary attested no two
			// people share (a person's sub, a key's holder row, an application);
			// a token with no sub, whose user is a username or a display name,
			// keys nothing, and the surface refuses it.
			user, _ := principal.SubjectOf(m)
			return hz.Principal{
				Org: p.Org, Project: p.Project, User: user,
				Name: c.Header(authz.HeaderUserName),
				// An admin of a held org (hanzo) is one more stranger to its members'
				// links, as to their sessions and machines (readerOf, targetOwns): the
				// org's members are customers, and nobody but its person governs a chat.
				Admin: principal.IsOrgAdmin(c) && !personal(p.Org),
				Super: principal.Super(c),
				// A person's own access token: an id_token, a token that names an
				// actor (`act`) and an API key hold no access token of the person's,
				// so the boundary names no Holder for them.
				Person: m.Kind == principal.Person && m.Stable && m.Holder != "",
				Cred:   credential(c),
			}, true
		},
		Audit: shareAudit(deps.Audit),
	}, aiCompleter{ai: deps.AI}, orchestratorTools{})
	return err
}

// shareAudit appends one record to the audit trail for each change a member
// makes: a share made or revoked, or a viewer removed, recorded on the share;
// a conversation renamed, pinned, archived or deleted (share ""), recorded on
// the conversation. A nil recorder records nothing here; the agent package
// still logs the change.
func shareAudit(rec *audit.Recorder) func(c *zip.Ctx, action, conversation, share string) {
	if rec == nil {
		return nil
	}
	return func(c *zip.Ctx, action, conversation, share string) {
		org, _ := principal.Org(c)
		resource := audit.Resource{Type: "conversation-share", ID: share}
		if share == "" {
			resource = audit.Resource{Type: "conversation", ID: conversation}
		}
		entry := audit.Record{
			Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
			Action:    action,
			Resource:  resource,
			Auth:      audit.AuthContext{Method: "gateway", IsAdmin: principal.Super(c)},
			Outcome:   audit.Outcome{Result: "ok", Status: http.StatusOK},
			Method:    c.Method(),
			Path:      c.Path(),
			SourceIP:  geo.IP(c),
			UserAgent: c.Header("User-Agent"),
			RequestID: c.RequestID(),
		}
		if after, err := json.Marshal(map[string]string{"conversation": conversation, "share": share}); err == nil {
			entry.After = after
		}
		if _, err := rec.Append(c.Context(), entry); err != nil {
			luxlog.Default().Warn("audit append failed", "err", err, "action", action)
		}
	}
}

// ── Completer: the AI subsystem, through the one client that reaches it ───────────

type aiCompleter struct{ ai infer.Client }

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
// header: Org is whose data this is and Payer is the account that pays, which is
// the same split infer.ChatRequest already documents and the same one a SuperAdmin
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
	in := &infer.ChatRequest{
		Model:     req.Model,
		Org:       org,
		Payer:     account.Parse(cred[scopePayer]),
		Project:   cloud.Who(ctx).Project,
		MaxTokens: req.MaxTokens,
		Messages:  inMessages(req.Messages),
		Tools:     inTools(req.Tools),
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
	if errors.Is(err, meter.ErrInsufficientBalance) || errors.Is(err, meter.ErrSpendCapExceeded) {
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
func inMessages(in []openai.ChatCompletionMessage) []infer.ChatMessage {
	out := make([]infer.ChatMessage, 0, len(in))
	for _, m := range in {
		msg := infer.ChatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				continue
			}
			msg.ToolCalls = append(msg.ToolCalls, infer.ToolCall{
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
func inTools(in []openai.Tool) []infer.ToolDef {
	out := make([]infer.ToolDef, 0, len(in))
	for _, t := range in {
		if t.Function == nil || t.Function.Name == "" {
			continue
		}
		schema, err := json.Marshal(t.Function.Parameters)
		if err != nil {
			continue
		}
		out = append(out, infer.ToolDef{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Schema:      schema,
		})
	}
	return out
}

// outMessage is the assistant turn the round reads: its words, and the calls it
// wants run before it can finish.
func outMessage(out *infer.ChatResponse) openai.ChatCompletionMessage {
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

// scopeOrg and scopePayer are where the caller's BILLING SCOPE rides to the
// completer: the org the turn reads, and the caller's account (principal.Payer),
// written the way IAM writes the billing_account claim so it parses back as the
// account it was. In the signup org that account is the person, never the pool.
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
	scopePayer = "hanzo.payer"
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
		cred[scopePayer] = meter.Payer(c).String()
	}
	return cred
}
