package guide

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// event is one step the Business AI agent takes, streamed to the caller (SSE) or
// collected into the JSON response. Types: plan|draft|action|result|state|error.
type event struct {
	Type   string         `json:"type"`
	Step   string         `json:"step,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Text   string         `json:"text,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Result any            `json:"result,omitempty"`
	State  State          `json:"state,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// agentDeps is the narrow surface the agent needs: the embedded AI (to draft
// content), the caller's per-org store (action ledger + state write), and the
// invoke seam onto the per-principal MCP plane. It is a struct of injected
// collaborators so runAgent is pure over them — the HTTP handler wires the real
// ones (deps.AI + automations.InvokeTool), tests wire fakes.
type agentDeps struct {
	ai     cloud.AIClient
	model  string
	store  *Store
	invoke func(ctx context.Context, org, tool string, args map[string]any) (any, error)
	toolOK func(tool string) bool
}

// runAgent executes a step's "do it for me". It drafts content with the embedded
// AI (when the step declares a Draft prompt), then runs the step's bound MCP tool
// AS THE CALLER'S ORG through the per-principal plane (d.invoke). Every tool call is
// recorded in the action ledger; a successful call marks the step done (source
// "agent") and arms the "acted" auto-detect signal. Each step is emitted via emit
// as it happens. It returns the step's final state.
//
// A step with no bound tool is ASSISTED, not automated: the agent drafts guidance
// (if a Draft prompt is present) and leaves the step in progress — it NEVER fakes a
// done for work it did not actually perform.
func runAgent(ctx context.Context, d agentDeps, org, payer string, step Step, emit func(event)) (State, error) {
	emit(event{Type: "plan", Step: step.ID, Text: planText(step)})

	// 1. Draft content with the embedded AI, if the step asks for it.
	var drafted string
	if strings.TrimSpace(step.Draft) != "" && d.ai != nil {
		res, err := d.ai.ChatCompletion(ctx, &cloud.ChatRequest{
			Model:      d.model,
			Prompt:     step.Draft,
			Org:        org,
			BillingOrg: payer,
		})
		if err != nil {
			emit(event{Type: "error", Step: step.ID, Error: "draft: " + err.Error()})
			return "", fmt.Errorf("draft: %w", err)
		}
		drafted = clipDraft(res.Content)
		emit(event{Type: "draft", Step: step.ID, Text: drafted})
	}

	// 2. No bound tool → assisted only. Honest: leave in progress, never mark done.
	if strings.TrimSpace(step.Tool) == "" {
		emit(event{Type: "result", Step: step.ID, Text: "Drafted guidance; this step has no automated action — complete it and mark it done."})
		if err := d.store.SetState(ctx, step.ID, StateInProgress, "agent", "assisted by Business AI", time.Now().Unix()); err != nil {
			return "", err
		}
		emit(event{Type: "state", Step: step.ID, State: StateInProgress})
		return StateInProgress, nil
	}

	// 3. Build the tool args: the step defaults, plus the drafted text folded into
	//    the DraftInto arg (default "brief").
	args := make(map[string]any, len(step.Args)+1)
	for k, v := range step.Args {
		args[k] = v
	}
	if drafted != "" {
		into := strings.TrimSpace(step.DraftInto)
		if into == "" {
			into = "brief"
		}
		args[into] = drafted
	}

	if d.toolOK != nil && !d.toolOK(step.Tool) {
		emit(event{Type: "error", Step: step.ID, Tool: step.Tool, Error: "unknown tool: " + step.Tool})
		return "", fmt.Errorf("unknown tool: %s", step.Tool)
	}
	emit(event{Type: "action", Step: step.ID, Tool: step.Tool, Args: args})

	// 4. Execute through the per-principal MCP plane AS THE CALLER (org).
	out, invErr := d.invoke(ctx, org, step.Tool, args)

	// 5. Record the action regardless of outcome (audit trail + the backing state
	//    for the "acted" signal).
	rec := ActionRecord{ID: newID(), StepID: step.ID, Tool: step.Tool, CreatedAt: time.Now().Unix()}
	if b, err := json.Marshal(args); err == nil {
		rec.Args = string(b)
	}
	if invErr != nil {
		rec.OK = false
		rec.Err = invErr.Error()
		_ = d.store.AddAction(ctx, rec)
		emit(event{Type: "error", Step: step.ID, Tool: step.Tool, Error: invErr.Error()})
		return "", invErr
	}
	if b, err := json.Marshal(out); err == nil {
		rec.Result = clipDraft(string(b))
	}
	rec.OK = true
	if err := d.store.AddAction(ctx, rec); err != nil {
		return "", err
	}
	emit(event{Type: "result", Step: step.ID, Tool: step.Tool, Result: out})

	// 6. The real effect landed → mark done (source "agent").
	if err := d.store.SetState(ctx, step.ID, StateDone, "agent", "completed by Business AI", time.Now().Unix()); err != nil {
		return "", err
	}
	emit(event{Type: "state", Step: step.ID, State: StateDone})
	return StateDone, nil
}

func planText(step Step) string {
	if strings.TrimSpace(step.Tool) == "" {
		return "Draft guidance for: " + step.Title
	}
	return "Draft content and run " + step.Tool + " for: " + step.Title
}

func clipDraft(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxDraftOutput {
		return s[:maxDraftOutput]
	}
	return s
}
