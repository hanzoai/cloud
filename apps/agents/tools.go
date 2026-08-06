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
// refuse every name. In the split fleet this process's own registry is not an
// answer; it is a fact about this process.
//
// The thing that CAN answer already exists, and it was already deployed: the
// fleet's composed agent door (fleet/mcp.go), which asks every app what it
// serves right now, merges the union, and forwards a call to the app that listed
// the name. It is what api.hanzo.ai/v1/mcp is. So there is one tool surface in
// this fleet and an agent reads THAT one — door.go is the client, over the
// host's own socket, and it is the plane a real deployment uses.
//
// registryTools stays as what this process's own registry says, which is the
// whole answer exactly where this process is the whole fleet: a single-app
// binary, a dev box, a test. doorTools falls back to it there and nowhere else,
// on the one signal that means it — nothing listening on the router's socket.
//
// The degradation that remains is an OUTAGE, and it is visible: every run's step
// span carries both hanzo.agent.tools_declared and hanzo.agent.tools, so
// "declared 3, offered 0" is a number in o11y rather than a silence, and the
// door's own error is recorded beside it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/tools"
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
// unchanged (door.go) rather than being redesigned at the seam.
type toolPlane interface {
	// catalog resolves the tool NAMES an agent declares into definitions the
	// model can be offered. A name that resolves to nothing is simply absent —
	// offering a tool that would be refused at dispatch teaches the model a lie.
	catalog(ctx context.Context, org, actor string, want []string) []types.ToolDef
	// call runs one tool as (org, actor) and returns its result as text. args is
	// the raw JSON object the model emitted, verbatim.
	call(ctx context.Context, org, actor, name, args string) (string, error)
}

// runTools is the tool plane a run uses: the fleet's own agent door, which
// answers with this process's registry wherever this process IS the fleet
// (door.go). A package var so a test can substitute a deterministic one; there
// is no exported setter, because which plane answers is a property of the
// deployment and not something a caller may choose.
var runTools toolPlane = doorTools{}

// registryTools is the tool plane read IN THIS PROCESS: tools.Default(), the same
// registry POST /v1/tools/call dispatches through, with the same activation gate,
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
// principal. The arguments are decoded into a map HERE, at the in-process seam
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
	return s[:maxToolResult] + "\n…[truncated: the result was longer than this agent may read]"
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
func completeWithTools(ctx context.Context, ai types.AIClient, org, actor, prompt, model, fallback string, defs []types.ToolDef) (*types.ChatResponse, string, error) {
	ctx, cancel := context.WithTimeout(ctx, toolRunBudget)
	defer cancel()

	msgs := []types.ChatMessage{{Role: types.RoleUser, Content: prompt}}
	used := model
	for round := 0; round <= maxToolRounds; round++ {
		offer := defs
		if round == maxToolRounds {
			offer = nil // budget spent — answer in words
		}
		resp, m, err := completeWithFailover(ctx, ai,
			&types.ChatRequest{Model: model, Org: org, Messages: msgs, Tools: offer}, fallback)
		used = m
		if err != nil {
			return nil, used, err
		}
		if resp == nil || len(resp.ToolCalls) == 0 || offer == nil {
			return resp, used, nil
		}
		msgs = append(msgs, types.ChatMessage{
			Role:      types.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, tc := range resp.ToolCalls {
			msgs = append(msgs, types.ChatMessage{
				Role:       types.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    dispatchOne(ctx, org, actor, tc),
			})
		}
	}
	// Unreachable: the round==maxToolRounds pass returns above whatever the model
	// does. Stated rather than assumed, so the loop has one exit per outcome.
	return nil, used, errors.New("agents: tool loop ended without an answer")
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
func dispatchOne(ctx context.Context, org, actor string, tc types.ToolCall) string {
	ctx, span := agentTracer.Start(ctx, "agent.tool", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.tool.name", tc.Name),
		attribute.String("hanzo.agent.org", org),
	)

	if len(tc.Arguments) > maxToolArgs {
		span.SetStatus(codes.Error, "arguments too large")
		return "error: the arguments for this call were too large to run"
	}
	ctx, cancel := context.WithTimeout(deeper(ctx), toolCallTimeout)
	defer cancel()

	out, err := runTools.call(ctx, org, actor, tc.Name, tc.Arguments)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed")
		return "error: " + err.Error()
	}
	if strings.TrimSpace(out) == "" {
		// An empty result and a failure look identical to a model reading a blank
		// string, and only one of them means "it worked".
		return "(the tool ran and returned nothing)"
	}
	return out
}
