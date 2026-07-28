package guide

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
)

// fakeAI is a minimal cloud.AIClient: ChatCompletion echoes a fixed draft so the
// agent's draft→arg fold is observable; Embed is unused.
type fakeAI struct {
	content string
	gotOrg  string
	gotPay  string
}

func (f *fakeAI) ChatCompletion(_ context.Context, req *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	f.gotOrg, f.gotPay = req.Org, req.BillingOrg
	return &cloud.ChatResponse{Content: f.content}, nil
}
func (f *fakeAI) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) { return nil, nil }

func collect() (func(event), *[]event) {
	var evs []event
	return func(e event) { evs = append(evs, e) }, &evs
}

func eventTypes(evs []event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// TestAgentToolStepSucceeds: the agent drafts with the AI, folds the draft into the
// tool arg, invokes the tool AS THE CALLER'S ORG, records the action, and marks the
// step done — arming the acted signal.
func TestAgentToolStepSucceeds(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	ai := &fakeAI{content: "DRAFTED COPY"}

	var gotOrg, gotTool string
	var gotArgs map[string]any
	invoke := func(_ context.Context, org, tool string, args map[string]any) (any, error) {
		gotOrg, gotTool, gotArgs = org, tool, args
		return map[string]any{"name": "Positioning-1"}, nil
	}
	d := agentDeps{ai: ai, model: "zen", store: st, invoke: invoke, toolOK: func(string) bool { return true }}
	step := Step{ID: "positioning", Title: "P", Tool: "content_generate", Draft: "write it", DraftInto: "brief", Args: map[string]any{"doctype": "Campaign"}}

	emit, evs := collect()
	final, err := runAgent(ctx, d, "acme", "acme", step, emit)
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if final != StateDone {
		t.Fatalf("final state want done, got %q", final)
	}
	// The tool ran as the caller's org, with the AI draft folded into "brief".
	if gotOrg != "acme" || gotTool != "content_generate" {
		t.Fatalf("invoked as org=%q tool=%q, want acme/content_generate", gotOrg, gotTool)
	}
	if gotArgs["brief"] != "DRAFTED COPY" || gotArgs["doctype"] != "Campaign" {
		t.Fatalf("args want {doctype:Campaign, brief:DRAFTED COPY}, got %v", gotArgs)
	}
	if ai.gotOrg != "acme" || ai.gotPay != "acme" {
		t.Fatalf("AI billed to org=%q payer=%q, want acme/acme", ai.gotOrg, ai.gotPay)
	}
	// The action is recorded (audit + acted-signal backing) and the step is done.
	acts, _ := st.ListActions(ctx, 10)
	if len(acts) != 1 || !acts[0].OK || acts[0].Tool != "content_generate" {
		t.Fatalf("expected one successful action, got %+v", acts)
	}
	states, _ := st.States(ctx)
	if states["positioning"].State != StateDone || states["positioning"].Source != "agent" {
		t.Fatalf("step must be done by agent, got %+v", states["positioning"])
	}
	// Event stream shape.
	if got := eventTypes(*evs); len(got) < 4 || got[0] != "plan" {
		t.Fatalf("event stream want plan…state, got %v", got)
	}
}

// TestAgentToolFailureNotDone: a tool failure records an error action and leaves the
// step NOT done — the agent never fakes a completion for work that failed.
func TestAgentToolFailureNotDone(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	invoke := func(context.Context, string, string, map[string]any) (any, error) {
		return nil, errors.New("slack not connected")
	}
	d := agentDeps{store: st, invoke: invoke, toolOK: func(string) bool { return true }}
	step := Step{ID: "email", Title: "E", Tool: "slack_send_message"}

	emit, evs := collect()
	final, err := runAgent(ctx, d, "acme", "acme", step, emit)
	if err == nil {
		t.Fatal("runAgent should surface the tool error")
	}
	if final == StateDone {
		t.Fatal("a failed tool must not mark the step done")
	}
	acts, _ := st.ListActions(ctx, 10)
	if len(acts) != 1 || acts[0].OK {
		t.Fatalf("expected one FAILED action, got %+v", acts)
	}
	states, _ := st.States(ctx)
	if states["email"].State == StateDone {
		t.Fatal("email step must not be done after a failed action")
	}
	lastType := (*evs)[len(*evs)-1].Type
	if lastType != "error" {
		t.Fatalf("last event want error, got %q", lastType)
	}
}

// TestAgentAssistedStepNoTool: a step with no bound tool is assisted (drafted) and
// left in progress — never auto-done.
func TestAgentAssistedStepNoTool(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	ai := &fakeAI{content: "here is your guidance"}
	d := agentDeps{ai: ai, store: st, invoke: func(context.Context, string, string, map[string]any) (any, error) {
		t.Fatal("invoke must NOT be called for a tool-less step")
		return nil, nil
	}}
	step := Step{ID: "referral", Title: "R", Draft: "advise"}

	emit, _ := collect()
	final, err := runAgent(ctx, d, "acme", "acme", step, emit)
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if final != StateInProgress {
		t.Fatalf("assisted step want in_progress, got %q", final)
	}
	if acts, _ := st.ListActions(ctx, 10); len(acts) != 0 {
		t.Fatalf("assisted step must record no tool action, got %+v", acts)
	}
}

// TestAgentUnknownToolRejected: an unknown tool is refused before any invoke,
// honestly, with no state change.
func TestAgentUnknownToolRejected(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	called := false
	d := agentDeps{store: st,
		invoke: func(context.Context, string, string, map[string]any) (any, error) { called = true; return nil, nil },
		toolOK: func(string) bool { return false }}
	step := Step{ID: "x", Title: "X", Tool: "made_up_tool"}

	if _, err := runAgent(ctx, d, "acme", "acme", step, func(event) {}); err == nil {
		t.Fatal("unknown tool should error")
	}
	if called {
		t.Fatal("invoke must not run for an unknown tool")
	}
}
