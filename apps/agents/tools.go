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
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/hanzoai/cloud/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// maxToolRounds is how many times the model may ask for tools in one run.
	// A loop is only as safe as its bound: each round is a completion the org
	// pays for, and a model that has decided to call the same tool forever will
	// do exactly that. Eight is deep enough for read-then-act-then-confirm and
	// shallow enough that a wedged agent costs a known amount.
	maxToolRounds = 8
	// toolRunBudget is the wall clock for the WHOLE loop, tools included. A Slack
	// turn is waiting on this, and a caller carrying a tighter deadline still
	// wins — this is a ceiling, never an extension.
	toolRunBudget = 90 * time.Second
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
	catalog(ctx context.Context, org, actor string, want []string) []types.ToolDef
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
func (registryTools) catalog(ctx context.Context, org, _ string, want []string) []types.ToolDef {
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
	out := make([]types.ToolDef, 0, len(wanted))
	for _, t := range tools.Default().List(ctx, tools.Scope{Org: org}) {
		if !wanted[t.Name] || !t.Dispatchable || !t.Activated {
			continue
		}
		out = append(out, types.ToolDef{Name: t.Name, Description: t.Description, Schema: t.Schema})
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
	return shorten.To(s, maxToolResult) + "\n…[truncated: the result was longer than this agent may read]"
}

// completeWithTools is the loop.
//
// It runs the conversation forward: complete, run whatever the model asked for,
// append the results, complete again — until the model answers in words, the
// round budget runs out, or the deadline does. Every completion goes through
// completeWithFailover, so the retry-and-fail-over reliability policy the run
// path already had applies to EVERY round rather than only the first, and the
// model reported back is the one that produced the final answer.
//
// The last round is offered no tools at all. A cap that simply stopped would
// return the model's last tool REQUEST as if it were an answer; offering nothing
// forces the model to say what it has, which is a real reply to the person
// waiting on it.
//
// It is HANDED the conversation rather than a prompt string. The loop's whole job
// is appending to a transcript, and one that began by wrapping a string in a
// single user turn could only ever be given the newest message — the turns before
// it had nowhere to go.
func completeWithTools(ctx context.Context, ai types.AIClient, org, actor string, msgs []types.ChatMessage, model, fallback string, defs []types.ToolDef, runID string) (*types.ChatResponse, string, error, int) {
	ctx, cancel := context.WithTimeout(ctx, toolRunBudget)
	defer cancel()

	used := model
	calls := 0
	for round := 0; round <= maxToolRounds; round++ {
		offer := defs
		if round == maxToolRounds {
			offer = nil // budget spent — answer in words
		}
		// EVERY round, not just the first: a tool-using run buys one completion
		// per round and each is its own usage row, so an actor stated once
		// would leave every round after it anonymous.
		resp, m, err := completeWithFailover(ctx, ai,
			&types.ChatRequest{Model: model, Org: org, Messages: msgs, Tools: offer, RunID: runID, Actor: actor}, fallback)
		used = m
		if err != nil {
			return nil, used, err, calls
		}
		if resp == nil || len(resp.ToolCalls) == 0 || offer == nil {
			return resp, used, nil, calls
		}
		msgs = append(msgs, types.ChatMessage{
			Role:      types.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, tc := range resp.ToolCalls {
			calls++
			msgs = append(msgs, types.ChatMessage{
				Role:       types.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    dispatchOne(ctx, org, actor, tc, runID, round),
			})
		}
	}
	// Unreachable: the round==maxToolRounds pass returns above whatever the model
	// does. Stated rather than assumed, so the loop has one exit per outcome.
	return nil, used, errors.New("agents: tool loop ended without an answer"), calls
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
func dispatchOne(ctx context.Context, org, actor string, tc types.ToolCall, runID string, round int) string {
	ctx, span := agentTracer().Start(ctx, "agent.tool "+tc.Name, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	// Everything an operator needs to read one dispatch out of a run: which run,
	// which tenant, which person, which tool, which subsystem answers for it, and
	// where in the loop it happened. The round is what makes "it called six tools
	// and failed on the fourth" a readable fact rather than an ordering guess
	// across spans that may be exported out of order.
	span.SetAttributes(
		attribute.String("gen_ai.tool.name", tc.Name),
		attribute.String("gen_ai.tool.call.id", tc.ID),
		// The tenant under the key the trace plane files rows by (planeOrg,
		// apps/o11y/planesink.go) — see the note on the run span in agents.go.
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.agent.run_id", runID),
		attribute.String("hanzo.agent.tool_subsystem", toolSubsystem(tc.Name)),
		attribute.Int("hanzo.agent.tool_round", round),
		// WHAT it was called with. Without this a trace says an agent called
		// "post_v1_exec_run" six times and cannot say what it ran — which is the
		// difference between knowing a run touched a tool and knowing what it did.
		// The convention's own name for this (opt-in precisely because it can carry
		// user data), redacted and bounded by recordable.
		attribute.String("gen_ai.tool.call.arguments", recordable(tc.Arguments)),
	)
	if sub := actorSub(org, actor); sub != "" {
		span.SetAttributes(attribute.String("hanzo.user", sub))
	}
	// The outcome is SET on every exit, including the happy one. A span whose
	// status is only ever written on failure cannot distinguish "succeeded" from
	// "never finished" — and a tool that hangs until the run's budget expires is
	// exactly the case an operator is looking for.
	outcome := "ok"
	defer func() { span.SetAttributes(attribute.String("hanzo.agent.tool_outcome", outcome)) }()

	if len(tc.Arguments) > maxToolArgs {
		outcome = "rejected"
		span.SetStatus(codes.Error, "arguments too large")
		return "error: the arguments for this call were too large to run"
	}
	if b := budgetFrom(ctx); b != nil {
		if err := b.affordTool(ctx, tc.Name); err != nil {
			outcome = "refused"
			span.SetStatus(codes.Error, "budget refused")
			return "error: " + err.Error()
		}
	}
	ctx, cancel := context.WithTimeout(deeper(ctx), toolCallTimeout)
	defer cancel()

	out, err := runTools.call(ctx, org, actor, tc.Name, tc.Arguments)
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed")
		return "error: " + err.Error()
	}
	// WHAT came back — the other half of the dispatch, and the half that explains
	// what the model did next. A tool that "succeeded" while returning an error
	// document is invisible without it.
	span.SetAttributes(attribute.String("gen_ai.tool.call.result", recordable(out)))
	span.SetStatus(codes.Ok, "")
	if strings.TrimSpace(out) == "" {
		// An empty result and a failure look identical to a model reading a blank
		// string, and only one of them means "it worked".
		return "(the tool ran and returned nothing)"
	}
	return out
}
