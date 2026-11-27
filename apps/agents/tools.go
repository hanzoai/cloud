package agents

// tools.go is the part of a run that was missing: Agent.Tools was stored,
// updated and shown, and the run never read it. An agent with "slack_post_message"
// in its tool list ran one chat completion against a model that had never been
// told the tool exists, so every @hanzo turn was a chatbot with no hands.
//
// A tool call is a CONVERSATION, not a call: the model asks for a tool, something
// runs it, the result goes back, and the model decides again. Three things make
// that loop safe to run on someone else's money —
//
//	BOUNDED     maxToolRounds model turns and one wall-clock budget for the whole
//	            run. The last turn is offered NO tools, so the loop cannot end in
//	            anything but words.
//	ATTRIBUTED  every dispatch carries the run's own (org, actor) — the same pair
//	            the run's fee is billed under — so a tool runs as the principal
//	            that asked for it and the tool plane meters it there.
//	RECOVERABLE a tool that fails is reported TO THE MODEL as a tool result, not
//	            raised. A broken connector makes the agent explain itself; it does
//	            not kill the turn.
//
// An agent with an empty Tools list never enters any of this: executeRun takes
// the same single completion it always did.
//
// ── WHERE THE TOOLS COME FROM ─────────────────────────────────────────────────
//
// A PLUGIN IS A PROCESS. `agents` ships as its own binary (plugin/agents/main.go)
// and `tools` as another (manifest/apps.go), so tools.Default() HERE holds only
// what agents itself registered — the agentToolProvider at agents.go:399 — and
// its activation store is nil, which makes ActivationStore.IsActivated report
// false for everything (apps/tools/activation.go:83) and Registry.Dispatch
// refuse every name. In the split surface this process's own registry is not an
// answer; it is a fact about this process.
//
// The thing that CAN answer already exists, and it was already deployed: the
// surface's composed agent MCP server (client/mcp.go), which asks every app what it
// serves right now, merges the union, and forwards a call to the app that listed
// the name. It is what api.hanzo.ai/v1/mcp is. So there is one tool surface in
// this surface and an agent reads THAT one — surface.go is the client, over the
// host's own socket, and it is the plane a real deployment uses.
//
// registryTools stays as what this process's own registry says, which is the
// whole answer exactly where this process is the whole surface: a single-app
// binary, a dev box, a test. surfaceTools falls back to it there and nowhere else,
// on the one signal that means it — nothing listening on the router's socket.
//
// The degradation that remains is an OUTAGE, and it is visible: every run's step
// span carries both hanzo.agent.tools_declared and hanzo.agent.tools, so
// "declared 3, offered 0" is a number in o11y rather than a silence, and the
// MCP server's own error is recorded beside it.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// spentNote is what the model is told when its rounds are gone. Offering no
// tools was meant to force an answer, and it did not: measured on enso, a
// transcript of eight tool calls followed by a tool-less request came back
// EMPTY, twice in a row, and the person was shown "(the agent returned an
// empty response)" for a turn that had done three minutes of real looking.
const spentNote = "You have used every tool round this turn allows. Answer the person now, " +
	"in words, from what you have found so far. Say plainly what is still unknown, " +
	"and that they can reply \"continue\" for you to keep going."

// silentNote is what a model that answered with nothing is told, once.
const silentNote = "Your last reply was empty. Answer the person now, in words. If the " +
	"answer needs live or looked-up information, call a tool for it instead."

// respCalls is the tool calls of a response that may be nil.
func respCalls(r *infer.ChatResponse) []infer.ToolCall {
	if r == nil {
		return nil
	}
	return r.ToolCalls
}

// outOfSteps is the answer a spent turn gives when the model still said
// nothing: what it looked at and how to carry on, never an empty message. The
// response's usage is kept, since the completion was bought either way.
func outOfSteps(resp *infer.ChatResponse, msgs []infer.ChatMessage) *infer.ChatResponse {
	var used []string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if !slices.Contains(used, tc.Name) {
				used = append(used, tc.Name)
			}
		}
	}
	say := "I couldn't put an answer into words that time."
	if len(used) > 0 {
		say = "I ran out of steps before I could finish answering (I looked with " + strings.Join(used, ", ") + ")."
	}
	say += " Reply \"continue\" and I will pick up from there."
	out := infer.ChatResponse{}
	if resp != nil {
		out = *resp
	}
	out.Content, out.ToolCalls = say, nil
	return &out
}

const (
	// maxToolRounds is how many times the model may ask for tools in one run.
	// A loop is only as safe as its bound: each round is a completion the org
	// pays for, and a model that has decided to call the same tool forever will
	// do exactly that. Eight is deep enough for read-then-act-then-confirm and
	// shallow enough that a wedged agent costs a known amount.
	maxToolRounds = 8
	// toolRunBudget is the wall clock for the WHOLE loop, tools included. A caller
	// carrying a tighter deadline still wins — this is a ceiling, never an
	// extension.
	//
	// It was 90 seconds, and 90 seconds was the BINDING deadline on every chat
	// turn: tighter than the turn's own budget and tighter than the AI client's,
	// so it — not either of them — is what a slow run died on. Successful runs
	// routinely take 35-85s, which puts the cap inside the spread of ordinary
	// work rather than outside it, and roughly one run in seven was cut
	// mid-completion with `context deadline exceeded`. A ceiling a normal run
	// reaches is not a safeguard against a wedged agent, it is a coin toss; what
	// actually bounds a loop that will not stop is the round cap (maxToolRounds)
	// and the per-dispatch timeout (toolCallTimeout), and both are unchanged.
	//
	// So this is only the OUTER stop and it sits well clear of the work. It is
	// also the LAST word on how long a run may take: a caller's transport may
	// bound its own goroutine, but nothing above sets a deadline meant to bound
	// the thinking, because nothing above can see what the thinking costs.
	toolRunBudget = 15 * time.Minute
	// modelBudget bounds ONE completion, and it is the bound that was missing.
	//
	// A loop budget cannot stand in for a per-call one. Eight rounds shared four
	// minutes, so a model that answered in a second left plenty and a model that
	// took forty seconds to its first token exhausted the run in six — and the
	// error, `chat completion: context deadline exceeded`, named the completion
	// while the deadline belonged to the loop. Enso failed exactly this way in
	// production: cold it took 39.5s to return sixteen tokens, warm it took 0.97s,
	// so the same question failed or answered depending on nothing the asker did.
	//
	// Four minutes is sized for TIME TO FIRST TOKEN on a local model, which can
	// reach two, plus the generation after it. It is deliberately generous: this
	// bound exists to catch a hung upstream, not to hurry a slow one, and a run
	// killed while the model is still producing is a run that spent its money and
	// threw the answer away.
	//
	// The three bounds are three different questions and must not be one number:
	// this one asks whether the model is answering, toolCallTimeout asks whether a
	// connector is, and toolRunBudget asks whether the whole thing has gone on too
	// long. Idle time between tokens is a fourth, and needs streaming to see —
	// ChatStream can, and this loop does not use it yet.
	modelBudget = 4 * time.Minute
	// toolCallTimeout bounds ONE dispatch, so a single hung connector cannot eat
	// the whole run's budget and starve the turn of its answer.
	toolCallTimeout = 30 * time.Second
	// maxToolResult bounds what one tool may put back into the transcript. A tool
	// that returns a megabyte would be paid for as prompt tokens on every
	// remaining round; the model is told the result was truncated.
	maxToolResult = 16 * 1024
	// maxToolArgs bounds the arguments a model may emit for one call, before they
	// are ever parsed.
	maxToolArgs = 32 * 1024
	// maxRecorded bounds what ONE tool call may put on its span — its arguments or
	// its result. A trace is for READING, not for replay, and this is deliberately
	// far tighter than maxToolResult: that bound is paid once as prompt tokens,
	// this one is stored for every call of every run and kept for the life of the
	// trace. 4 KiB is a page of evidence. What exceeds it is cut and SAID to be
	// cut — a silently clipped value reads as the whole argument, which is how an
	// operator concludes a tool was called with something it never saw.
	maxRecorded = 4 * 1024
	// maxStatus bounds the failure a span's STATUS carries. A status is a one-line
	// summary a trace list renders inline, so it is much tighter than maxRecorded;
	// the full error is already on the recorded event beside it.
	maxStatus = 200
	// maxAgentDepth bounds how deep AGENTS may nest, which is a different bound
	// from maxToolRounds and is not covered by it: an agent is itself a tool
	// (agentToolProvider, agents.go:399), so A calling B calling A is a cycle in
	// which every level gets a FRESH round cap and a fresh fee. Three levels is
	// an agent delegating to a specialist that delegates once more; deeper than
	// that is a loop, and at the bottom an agent is simply offered no tools and
	// has to answer for itself.
	maxAgentDepth = 3
)

// depthKey carries how many agents deep this run is. Unexported zero-size type,
// so nothing outside this package can forge a shallower depth.
type depthKey struct{}

// agentDepth reads the nesting depth off the context; a top-level run is 0.
func agentDepth(ctx context.Context) int {
	d, _ := ctx.Value(depthKey{}).(int)
	return d
}

// deeper marks the context one agent deeper. It is applied at the DISPATCH, so
// the depth travels with the call that creates the nesting — a nested run reads
// it from the context its parent's tool call handed it.
func deeper(ctx context.Context) context.Context {
	return context.WithValue(ctx, depthKey{}, agentDepth(ctx)+1)
}

// callableTools is what an agent may actually be offered: its declared names,
// minus the one that would call the agent ITSELF. A self-call is a recursion no
// round cap bounds, because each level starts its cap over.
func callableTools(a Agent) []string {
	self := "agent_" + a.Name
	out := make([]string, 0, len(a.Tools))
	for _, n := range a.Tools {
		if strings.TrimSpace(n) == self {
			continue
		}
		out = append(out, n)
	}
	return out
}

// toolPlane is where a run's callable tools come from: what may be offered to
// the model, and what happens when it asks for one.
//
// It is an interface for the reason the package comment gives — the answer is
// per-DEPLOYMENT, not per-run — and it is deliberately narrow: names, prose,
// schemas, and one call that takes raw JSON in and returns text out. Nothing in
// it is a map, which is what let the same shape cross a process boundary
// unchanged (surface.go) rather than being redesigned at the client.
type toolPlane interface {
	// catalog resolves the tool NAMES an agent declares into definitions the
	// model can be offered. A name that resolves to nothing is simply absent —
	// offering a tool that would be refused at dispatch teaches the model a lie.
	catalog(ctx context.Context, org, actor string, want []string) []infer.ToolDef
	// call runs one tool as (org, actor) and returns its result as text. args is
	// the raw JSON object the model emitted, verbatim.
	call(ctx context.Context, org, actor, name, args string) (string, error)
}

// runTools is the tool plane a run uses: the surface's own agent MCP server, which
// answers with this process's registry wherever this process IS the surface
// (surface.go). A package var so a test can substitute a deterministic one; there
// is no exported setter, because which plane answers is a property of the
// deployment and not something a caller may choose.
var runTools toolPlane = surfaceTools{}

// registryTools is the tool plane read IN THIS PROCESS: tools.Default(), the same
// registry POST /v1/tool/call dispatches through, with the same activation gate,
// the same source precedence and the same x402 settlement. It is the whole answer
// where the tool plane is co-resident, and it is honest where it is not — the
// registry simply offers nothing.
type registryTools struct{}

// catalog keeps a declared name only when the plane offers it to this org AND it
// is dispatchable AND it is activated. All three are the conditions dispatch
// itself enforces (apps/tools/registry.go:231), so a tool that survives this
// filter is one the model can actually call — which is the only kind worth
// spending a prompt on.
func (registryTools) catalog(ctx context.Context, org, _ string, want []string) []infer.ToolDef {
	if org == "" || len(want) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(want))
	for _, n := range want {
		if n = strings.TrimSpace(n); n != "" {
			wanted[n] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	out := make([]infer.ToolDef, 0, len(wanted))
	for _, t := range tools.Default().List(ctx, tools.Scope{Org: org}) {
		if !wanted[t.Name] || !t.Dispatchable || !t.Activated {
			continue
		}
		out = append(out, infer.ToolDef{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	return out
}

// call dispatches through the registry's ONE policy path, bound to the run's own
// principal. The arguments are decoded into a map HERE, at the in-process client
// that requires one, and nowhere else — the map never appears on a type that has
// to cross a process boundary.
func (registryTools) call(ctx context.Context, org, actor, name, args string) (string, error) {
	var decoded map[string]any
	if s := strings.TrimSpace(args); s != "" && s != "null" {
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return "", fmt.Errorf("arguments are not a JSON object: %w", err)
		}
	}
	out, err := tools.Default().Dispatch(ctx, tools.Principal{Org: org, User: actorSub(org, actor)}, name, decoded)
	if err != nil {
		return "", err
	}
	return renderToolResult(out), nil
}

// toolSubsystem names the app that answers for a tool, read out of the tool's OWN
// name rather than looked up anywhere.
//
// The surface MCP server groups one tool per subsystem and carries the operation
// names in its `op` enum (client/grouped.go), and those names are spelled
// <method>_<subsystem>_<rest> — so the owner is a fact the name already states.
//
// THE METHOD IS WHAT SAYS THE NAME IS IN THAT SHAPE. Reading the second word
// unconditionally would answer "web" for a registry-local "search_web", so the
// leading word must first be a method for the one after it to be an owner.
// Deriving it here keeps this a pure function of the value: it answers the same
// way in the fused binary and in a single-app plugin process, whereas
// cloud.SubsystemOf reads a boot-time mount index that in a plugin knows only
// that plugin's own routes and would answer "" for every sibling's tool.
//
// THE SUBSYSTEM IS THE SECOND WORD, because the first is the method and the
// version is not in the name: zip.ID drops a leading /v1 as saying nothing that
// every address does not already carry. This used to read whatever followed a "v1"
// word, so once the names lost it every tool answered "" and every tool span lost
// the app it addressed. An op that declares its own id may still spell the version
// (plugin/agents declares post_v1_coding), so a second word of "v1" is stepped
// over rather than returned.
//
// A name that is not in that shape (a registry-local tool like "http") owns no
// subsystem and says so with "", rather than with a guess.
func toolSubsystem(op string) string {
	parts := strings.Split(op, "_")
	if len(parts) < 2 || !httpMethodWord(parts[0]) {
		return ""
	}
	if parts[1] == "v1" {
		if len(parts) < 3 {
			return ""
		}
		return parts[2]
	}
	return parts[1]
}

// httpMethodWord reports whether a word is a method as [zip.ID] spells one: the
// lowercase name, leading an operation id.
func httpMethodWord(s string) bool {
	switch s {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return true
	}
	return false
}

// actorSub reads the user subject back out of the run's "org/sub" billing actor,
// so a dispatch runs as the person the run is billed to. A bare org (a scheduled
// run, a service token) has no subject and lends none: the org is the authority
// either way, and inventing a user would be attributing the call to nobody.
func actorSub(org, actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" || actor == org {
		return ""
	}
	if sub, ok := strings.CutPrefix(actor, org+"/"); ok {
		return sub
	}
	return actor
}

// recordable is what a tool call may put on a span: credentials stripped, size
// bounded, and the bound stated.
//
// Both halves are load-bearing. A tool ARGUMENT routinely carries a live
// credential — a clone URL with a token in the userinfo is the ordinary shape
// (apps/coding builds exactly that) — and a trace that records one is worse than
// no trace at all, because the token outlives the sandbox that used it and sits
// in a store built to be queried. audit.RedactText is the surface's ONE redactor;
// this adds no second policy, it just applies it at the moment of recording.
//
// The order matters: redact FIRST, then cut. Cutting first can split a credential
// and leave the front half of it on the span, which is both a leak and unreadable.
func recordable(s string) string {
	s = audit.RedactText(s)
	if len(s) <= maxRecorded {
		return s
	}
	// Cut on a rune boundary so the value stays valid UTF-8; a span store that
	// rejects or mangles invalid UTF-8 would lose the whole attribute over the
	// last byte of a multi-byte character.
	cut := maxRecorded
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\u2026[cut: longer than a span records]"
}

// renderToolResult turns whatever a tool returned into the text the model reads.
// A string is already text; anything else is its JSON, which is the shape the
// tool's own schema describes. A value that will not marshal is reported as that
// fact rather than as an empty result the model would read as success.
func renderToolResult(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return truncateToolResult(t)
	case []byte:
		return truncateToolResult(string(t))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "the tool returned a value that could not be encoded"
	}
	return truncateToolResult(string(b))
}

// truncateToolResult bounds one tool result and SAYS SO. A silently clipped result is a
// result the model believes it read in full.
func truncateToolResult(s string) string {
	if len(s) <= maxToolResult {
		return s
	}
	return text.Cut(s, maxToolResult) + "\n…[truncated: the result was longer than this agent may read]"
}

// completeWithTools is the loop.
//
// It runs the conversation forward: complete, run whatever the model asked for,
// append the results, complete again — until the model answers in words, the
// round budget runs out, or the deadline does. Every completion goes through
// completeWithFailover, so the retry-and-fail-over reliability policy the run
// path already had applies to EVERY round rather than only the first, and the
// model reported back is the one that produced the final answer. The turn keeps
// the model that answered its first round, and a model it has to move to starts
// on a fresh reasoning context (pin.go).
//
// The last round is offered no tools at all. A cap that simply stopped would
// return the model's last tool REQUEST as if it were an answer; offering nothing
// forces the model to say what it has, which is a real reply to the person
// waiting on it.
//
// A TURN IS LISTENING while it runs (steer.go): what its person says is added to
// the conversation before each round, and an answer is final only once nothing
// said is waiting. So the loop ends on an answer and on nothing else, and it is
// bounded by the rounds, by what was actually said, and by the budget.
//
// An agent with no tools is the same loop offered nothing: one completion, and a
// round more for anything said while it was written.
//
// It is HANDED the conversation rather than a prompt string. The loop's whole job
// is appending to a transcript, and one that began by wrapping a string in a
// single user turn could only ever be given the newest message — the turns before
// it had nowhere to go.
func completeWithTools(ctx context.Context, ai infer.Client, org, actor string, msgs []infer.ChatMessage, model, fallback string, defs []infer.ToolDef, runID string) (*infer.ChatResponse, string, error, int) {
	ctx, cancel := context.WithTimeout(ctx, toolRunBudget)
	defer cancel()

	used := model
	tm := &turnModel{own: model, fallback: fallback}
	calls := 0
	// A LARGE SURFACE IS OFFERED ON REQUEST, NOT UP FRONT. The assistant's tools
	// are the whole cloud, and their schemas are tens of thousands of tokens that
	// every completion reads before it writes a word — measured, 27,319 of them
	// on a turn that said "hi", and a first completion of two minutes. So the
	// opening hop carries one small tool, `tools`, and the model calls it when
	// answering needs the cloud; the catalog is handed over then and the loop goes
	// on as before. Conversation never asks, and is answered at the speed of a
	// prompt its own size.
	staged := len(defs) > stageAbove
	l := liveFrom(ctx)
	// A turn with a person to ask is offered ask_user beside whatever else it has
	// (ask.go), staged or not: asking is conversation, not a cloud operation.
	asks := l.asks(ctx)
	coord := coordFrom(ctx)
	nudged := false
	lastSig := ""
	repeatCount := 0
	for round := 0; ; round++ {
		// What the person said into the turn since the model last looked, as their
		// next words (steer.go). Between rounds, never inside one: a tool result
		// has to follow the call that asked for it.
		msgs = append(msgs, l.heard(ctx, false)...)
		offer := defs
		if staged {
			offer = []infer.ToolDef{loadTools}
		}
		if asks {
			offer = append(offer[:len(offer):len(offer)], AskTool)
		}
		if coord != nil {
			offer = append(offer[:len(offer):len(offer)], CoordinateTool)
		}
		spent := round >= maxToolRounds
		if spent {
			offer = nil // budget spent — answer in words
			if round == maxToolRounds {
				msgs = append(msgs, infer.ChatMessage{Role: infer.RoleSystem, Content: spentNote})
			}
		}
		// EVERY round, not just the first: a tool-using run buys one completion
		// per round and each is its own usage row, so an actor stated once
		// would leave every round after it anonymous.
		// The per-call budget lives one level down, on each HOP of the route,
		// because a route with a fallback needs the fallback to still have time.
		// The model holding the turn first; the route's other model only when it
		// cannot answer (pin.go).
		first, next := tm.route()
		resp, m, err := completeWithFailover(ctx, ai,
			&infer.ChatRequest{Model: first, Org: org, Messages: msgs, Tools: offer, RunID: runID, Actor: actor}, next, tm)
		used = m
		if err != nil {
			return nil, used, err, calls
		}
		if tm.served(ctx, m, round) {
			// The rest of the turn is the new model's, on the fresh context it was
			// just shown: the reasoning the previous model wrote stays behind.
			msgs = freshContext(msgs)
		}
		silent := resp == nil || strings.TrimSpace(resp.Content) == ""
		if silent && len(respCalls(resp)) == 0 && !spent && !nudged {
			// An answer of NO words, with nothing called: measured on enso, a
			// reasoning arm spent 786 tokens thinking about "weather in SF?" and
			// returned empty content with no tool call. One round, told so, before
			// the turn gives up; asked again it answers or reaches for the tool.
			nudged = true
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleSystem, Content: silentNote})
			continue
		}
		if resp == nil || len(resp.ToolCalls) == 0 || offer == nil {
			// An answer — unless the person spoke while it was being written. That is
			// owed a round of its own, and the last look is what closes the turn to
			// more (steer.go), so nothing said falls between this answer and the end.
			more := l.heard(ctx, true)
			if len(more) == 0 {
				if silent {
					resp = outOfSteps(resp, msgs)
				}
				return resp, used, nil, calls
			}
			if resp != nil && strings.TrimSpace(resp.Content) != "" {
				msgs = append(msgs, infer.ChatMessage{Role: infer.RoleAssistant, Content: resp.Content})
			}
			msgs = append(msgs, more...)
			continue
		}
		recorded := make([]infer.ToolCall, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			if a := strings.TrimSpace(tc.Arguments); a == "" || !json.Valid([]byte(a)) {
				tc.Arguments = "{}"
			}
			recorded[i] = tc
		}
		msgs = append(msgs, infer.ChatMessage{
			Role:      infer.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: recorded,
		})
		if staged && asksForTools(resp.ToolCalls) {
			// The catalog is the answer to the ask. It costs no round and counts as
			// no call: nothing was done, the model was handed what it asked for.
			staged = false
			for _, tc := range resp.ToolCalls {
				msgs = append(msgs, infer.ChatMessage{Role: infer.RoleTool, ToolCallID: tc.ID, Name: tc.Name,
					Content: "The cloud's tools are loaded. Call the one you need."})
			}
			round--
			continue
		}
		l.said(ctx, resp.Content)
		var roundImages []string
		for _, tc := range resp.ToolCalls {
			calls++
			l.doing(ctx, tc.Name)
			sig := tc.Name + "\x00" + strings.TrimSpace(tc.Arguments)
			if sig == lastSig {
				repeatCount++
			} else {
				lastSig = sig
				repeatCount = 1
			}
			var out, img string
			switch {
			case repeatCount > 2:
				out = "error: duplicate tool call (" + tc.Name + " with identical arguments). You already ran this exact call and received its result above; do not repeat it. Use the result you already have or take the next step."
			case asks && tc.Name == AskTool.Name:
				out = l.ask(ctx, tc.Arguments) // waits on the person, not on a tool
			case coord != nil && tc.Name == CoordinateTool.Name:
				out = coord.dispatch(deeper(ctx), tc.Arguments)
			default:
				out, img = dispatchOneWithMedia(ctx, org, actor, tc, runID, round)
			}
			if img != "" {
				roundImages = append(roundImages, img)
			}
			l.recordTool(tc.Name, tc.Arguments, out)
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleTool, ToolCallID: tc.ID, Name: tc.Name, Content: out})
		}
		if len(roundImages) > 0 {
			msgs = appendScreenImages(msgs, roundImages)
		}
	}
}

// dispatchOne runs one tool call and returns the text the model is handed —
// SUCCESS OR FAILURE, always as a tool result. A tool that fails is a fact the
// model can act on (try another one, or explain), and raising it instead would
// throw away a turn the org has already paid for.
//
// It carries no credential. Tool credentials live in KMS behind the tool plane
// and are resolved by the source that owns them, so nothing secret is in scope
// here to leak into a transcript: what goes back is the tool's own output or our
// own sentence about why there is none.
func dispatchOne(ctx context.Context, org, actor string, tc infer.ToolCall, runID string, round int) string {
	out, _ := dispatchOneWithMedia(ctx, org, actor, tc, runID, round)
	return out
}

const screenFramePrompt = "Current desktop screen."

// appendScreenImages attaches desktop screenshots after all tool results in the
// round while pruning older desktop frames so at most the two most recent
// screenshots stay in the prompt.
func appendScreenImages(msgs []infer.ChatMessage, images []string) []infer.ChatMessage {
	msgs = append(msgs, infer.ChatMessage{
		Role:    infer.RoleUser,
		Content: screenFramePrompt,
		Images:  images,
	})
	const keepFrames = 2
	kept := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == infer.RoleUser && msgs[i].Content == screenFramePrompt && len(msgs[i].Images) > 0 {
			kept += len(msgs[i].Images)
			if kept > keepFrames {
				msgs[i].Images = nil
				msgs[i].Content = "(earlier desktop screen omitted)"
			}
		}
	}
	return msgs
}

func parseComputerIn(name, args string) (client.ComputerIn, bool) {
	if name == opComputer {
		var in client.ComputerIn
		if json.Unmarshal([]byte(args), &in) != nil {
			return client.ComputerIn{}, false
		}
		return in, true
	}
	if name == sandboxTool {
		var env struct {
			Op    string            `json:"op"`
			Input client.ComputerIn `json:"input"`
		}
		if json.Unmarshal([]byte(args), &env) != nil || env.Op != opComputer {
			return client.ComputerIn{}, false
		}
		return env.Input, true
	}
	return client.ComputerIn{}, false
}

func extractComputerMedia(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "{") || !strings.Contains(s, "data:image/") {
		return raw, ""
	}
	var co client.ComputerOut
	if json.Unmarshal([]byte(s), &co) != nil || !strings.HasPrefix(co.Image, "data:image/") {
		return raw, ""
	}
	img := co.Image
	co.Image = ""
	b, err := json.Marshal(co)
	if err != nil {
		return raw, img
	}
	return string(b), img
}

func dispatchOneWithMedia(ctx context.Context, org, actor string, tc infer.ToolCall, runID string, round int) (string, string) {
	ctx, span := agentTracer().Start(ctx, "agent.tool "+tc.Name, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.tool.name", tc.Name),
		attribute.String("gen_ai.tool.call.id", tc.ID),
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.agent.run_id", runID),
		attribute.String("hanzo.agent.tool_subsystem", toolSubsystem(tc.Name)),
		attribute.Int("hanzo.agent.tool_round", round),
		attribute.String("gen_ai.tool.call.arguments", recordable(tc.Arguments)),
	)
	if sub := actorSub(org, actor); sub != "" {
		span.SetAttributes(attribute.String("hanzo.user", sub))
	}
	outcome := "ok"
	defer func() { span.SetAttributes(attribute.String("hanzo.agent.tool_outcome", outcome)) }()

	if len(tc.Arguments) > maxToolArgs {
		outcome = "rejected"
		span.SetStatus(codes.Error, "arguments too large")
		return "error: the arguments for this call were too large to run", ""
	}
	if b := budgetFrom(ctx); b != nil {
		if err := b.affordTool(ctx, tc.Name); err != nil {
			outcome = "refused"
			span.SetStatus(codes.Error, "budget refused")
			return "error: " + err.Error(), ""
		}
	}
	args, err := boxCall(ctx, tc.Name, tc.Arguments)
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, "sandbox: "+text.Cut(audit.RedactText(err.Error()), maxStatus))
		return "error: " + err.Error(), ""
	}
	args, err = furnish(ctx, tc.Name, args)
	if err != nil {
		outcome = "refused"
		span.SetStatus(codes.Error, err.Error())
		return "error: " + err.Error(), ""
	}
	ctx, cancel := context.WithTimeout(deeper(ctx), toolCallTimeout)
	defer cancel()

	args, err = attest(ctx, tc.Name, address(ctx, tc.Name, args))
	if err != nil {
		outcome = "refused"
		span.SetStatus(codes.Error, err.Error())
		return "error: " + err.Error(), ""
	}

	var out, img string
	if _, isSurface := runTools.(surfaceTools); isSurface {
		if cin, ok := parseComputerIn(tc.Name, args); ok && strings.TrimSpace(cin.ID) != "" {
			pctx := zip.WithCaller(ctx, zip.Caller{Org: org, User: actorSub(org, actor)})
			cout, cerr := sandboxpeer.SandboxComputer(pctx, &cin)
			if cerr != nil {
				err = cerr
			} else {
				img = cout.Image
				cout.Image = ""
				b, _ := json.Marshal(cout)
				out = string(b)
			}
		} else {
			out, err = runTools.call(ctx, org, actor, tc.Name, args)
		}
	} else {
		out, err = runTools.call(ctx, org, actor, tc.Name, args)
	}
	if err == nil && img == "" {
		out, img = extractComputerMedia(out)
	}
	liveFrom(ctx).coded(tc.Name, args, out, err)
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed: "+text.Cut(audit.RedactText(err.Error()), maxStatus))
		return "error: " + err.Error(), ""
	}
	out = truncateToolResult(out)
	span.SetAttributes(attribute.String("gen_ai.tool.call.result", recordable(out)))
	span.SetStatus(codes.Ok, "")
	if strings.TrimSpace(out) == "" {
		return "(the tool ran and returned nothing)", img
	}
	return out, img
}

// stageAbove is how many tools an agent may have before they are offered on
// request. A handful of schemas is a small prompt and is simply offered; the
// assistant's whole-cloud surface is not.
const stageAbove = 8

// loadTools is the one tool the opening hop carries for a large surface.
var loadTools = infer.ToolDef{
	Name: "tools",
	Description: "Load the Hanzo cloud's tools: deployments, git and repositories, projects, billing and usage, " +
		"agents and coding runs, search and the web, and every other service the org runs. Call this when " +
		"answering needs to look something up or do something in the cloud. Do not call it for conversation, " +
		"for questions you can answer from what has been said, or to describe what you could do.",
	Schema: json.RawMessage(`{"type":"object","properties":{"why":{"type":"string","description":"What you need the tools for, in a few words."}},"additionalProperties":false}`),
}

// asksForTools reports whether the model's calls are the request for the
// catalog. Offered only that tool it can call nothing else, but it may call it
// more than once in one reply.
func asksForTools(calls []infer.ToolCall) bool {
	for _, tc := range calls {
		if tc.Name != loadTools.Name {
			return false
		}
	}
	return len(calls) > 0
}
