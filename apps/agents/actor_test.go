package agents

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// TestOnBehalfNamesThePersonOnTheModelCall is the whole point of the change: a
// Slack turn's linked subject has to reach the MODEL REQUEST, not just the run
// row and the tool plane.
//
// It ran as the deployment's own IAM application before, because the request the
// runner built had nowhere to put a person — so the gateway recorded the
// application as the spender and 52% of spend had no human owner. A run executes
// on a detached context, so nothing downstream can recover the person: if it is
// not on this request, it is gone.
func TestOnBehalfNamesThePersonOnTheModelCall(t *testing.T) {
	ai := &fakeAI{content: "answered"}
	app := mountApp(t, ai)
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "hanzo", "model": "gpt-4o-mini", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}

	if _, err := RunOnBehalf(context.Background(), "acme", "U-slack-123", "hanzo", "hi"); err != nil {
		t.Fatalf("RunOnBehalf: %v", err)
	}
	if got, want := ai.gotActor, "acme/U-slack-123"; got != want {
		t.Fatalf("model call actor = %q, want %q — the asker's identity was dropped "+
			"between the run and the gateway, which is what makes a usage row name "+
			"the hanzo-cloud application instead of a person", got, want)
	}
}

// TestRunNamesTheCaller proves the same for the ordinary HTTP run: the actor the
// run is recorded and gated under is the actor the completion is bought for, so
// one run never names two principals.
func TestRunNamesTheCaller(t *testing.T) {
	ai := &fakeAI{content: "answered"}
	app := mountApp(t, ai)
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "hanzo", "model": "gpt-4o-mini", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/hanzo/run", "acme",
		map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run want 200")
	}
	// do() mints X-User-Id as "u-<org>", which is what the gateway would send.
	if got, want := ai.gotActor, billingActor("acme", "u-acme"); got != want {
		t.Fatalf("model call actor = %q, want the run's own actor %q", got, want)
	}
}

// TestScheduledRunNamesItsAgent covers the surface with no person behind it. A
// scheduled run is bought by the platform for an agent, and the honest answer is
// to say which agent — not to invent a person, and not to fall back to the
// application, which names nobody at all.
func TestScheduledRunNamesItsAgent(t *testing.T) {
	ai := &fakeAI{content: "tick"}
	a := Agent{ID: "a1", Org: "acme", Name: "nightly", Model: "gpt-4o-mini"}
	r := executeRun(context.Background(), ai, a.Org, scheduledActor(a), a, "", nil, "", "run-1")
	if r.Status != "ok" {
		t.Fatalf("run status = %q, want ok", r.Status)
	}
	if got, want := ai.gotActor, scheduledActor(a); got != want {
		t.Fatalf("scheduled model call actor = %q, want %q", got, want)
	}
	if got := ai.gotActor; got == "" {
		t.Fatal("a scheduled run must still name the principal that caused the spend")
	}
}

// TestEveryToolRoundNamesThePerson is the round-by-round half. A tool-using run
// buys ONE completion PER ROUND and each is its own usage row, so naming the
// person on the first request only would leave every round after it anonymous —
// and a tool-using run is exactly the expensive kind.
func TestEveryToolRoundNamesThePerson(t *testing.T) {
	defs := []types.ToolDef{{Name: "lookup", Description: "d"}}
	plane := &fakePlane{offer: defs, out: "result"}
	withPlane(t, plane)

	ai := &scriptAI{replies: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{{ID: "c1", Name: "lookup", Arguments: "{}"}}},
		{ToolCalls: []types.ToolCall{{ID: "c2", Name: "lookup", Arguments: "{}"}}},
		{Content: "done"},
	}}

	const actor = "acme/U-slack-123"
	resp, _, err, calls := completeWithTools(context.Background(), ai, "acme", actor,
		[]types.ChatMessage{{Role: types.RoleUser, Content: "hi"}}, "gpt-4o-mini", "", defs, "run-1")
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if resp == nil || resp.Content != "done" || calls != 2 {
		t.Fatalf("loop did not run as scripted: resp=%+v calls=%d", resp, calls)
	}
	if len(ai.seen) != 3 {
		t.Fatalf("want 3 rounds, got %d", len(ai.seen))
	}
	for i, req := range ai.seen {
		if req.Actor != actor {
			t.Fatalf("round %d actor = %q, want %q — a later round bought inference "+
				"nobody was named for", i, req.Actor, actor)
		}
	}
	// The tool plane and the model must run as ONE principal, or a run's spend
	// and its side effects are attributed to two different people.
	for i, c := range plane.calls {
		if c.actor != actor {
			t.Fatalf("tool call %d actor = %q, want %q", i, c.actor, actor)
		}
	}
}
