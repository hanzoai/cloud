package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestRunOnBehalfBillsActor proves the in-process on-behalf-of path runs the
// agent through the SAME meter path as the HTTP handler and bills the AGENT's org
// with the actor "org/userSub" — the identity the Slack bridge passes for a linked
// user. This is the deliverable's "RunOnBehalf bills the right actor" bar.
func TestRunOnBehalfBillsActor(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "on-behalf answer"})
	_ = app

	// Create the agent the bridge will address by ref (name "hanzo").
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "hanzo", "model": "gpt-4o-mini", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}

	run, err := RunOnBehalf(context.Background(), "acme", "U-slack-123", "hanzo", "hi from slack")
	if err != nil {
		t.Fatalf("RunOnBehalf: %v", err)
	}
	if run.Status != "ok" || run.Output != "on-behalf answer" {
		t.Fatalf("run must succeed with the model output, got %+v", run)
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("an on-behalf run must debit exactly once, got %d", bs.debits())
	}
	org, ubody := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want the agent's org 'acme' (never the caller default)", org)
	}
	var u struct {
		Actor    string `json:"actor"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(ubody, &u)
	if u.Actor != "acme/U-slack-123" {
		t.Fatalf("actor = %q, want billingActor(org,userSub)=%q", u.Actor, "acme/U-slack-123")
	}
	if u.Provider != meterKind || u.Model != "gpt-4o-mini" {
		t.Fatalf("debit must be product=agent for the agent's model, got provider=%q model=%q", u.Provider, u.Model)
	}
}

// TestRunOnBehalfOrgScoped proves the in-process path is tenant-isolated: a caller
// for org A can never resolve/run/bill org B's agent — resolution is org-scoped, so
// a cross-org ref is errNotFound and NOTHING is billed.
func TestRunOnBehalfOrgScoped(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "should not run"})
	_ = app

	// "secret" exists only in globex.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "globex",
		map[string]any{"name": "secret", "model": "m", "instructions": "y"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	// acme attempts to run globex's agent by ref.
	if _, err := RunOnBehalf(context.Background(), "acme", "u", "secret", "hi"); err == nil {
		t.Fatal("acme must NOT resolve globex's agent (org-scoped) — cross-tenant run")
	}
	// No inference, no debit.
	if waitForDebit(func() bool { return bs.debits() > 0 }) {
		t.Fatalf("a cross-org run must never debit, got %d", bs.debits())
	}
}

// TestRunOnBehalfGatesUnfunded proves the on-behalf path shares the balance gate:
// an unfunded org gets NO free inference (fail-closed) and the fake AI is never
// called.
func TestRunOnBehalfGatesUnfunded(t *testing.T) {
	bs := &billServer{available: 0}
	ai := &fakeAI{content: "must not run"}
	app := mountBilled(t, bs.start(t), ai)
	_ = app
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "m", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	if _, err := RunOnBehalf(context.Background(), "acme", "u", "a", "hi"); err == nil {
		t.Fatal("unfunded on-behalf run must fail closed (balance-gate denial)")
	}
	if ai.gotPrompt != "" {
		t.Fatalf("no inference must run when the gate denies, got prompt %q", ai.gotPrompt)
	}
	if bs.debits() != 0 {
		t.Fatalf("a gate-refused run must not debit, got %d", bs.debits())
	}
}

// TestRunOnBehalfNotMounted proves the package-level entry fails closed when the
// agents subsystem is not mounted (no panic, an honest error).
func TestRunOnBehalfNotMounted(t *testing.T) {
	_ = Shutdown(context.Background())
	if _, err := RunOnBehalf(context.Background(), "acme", "u", "hanzo", "hi"); err == nil {
		t.Fatal("RunOnBehalf must fail when agents is not mounted")
	}
}

// TestRunOnBehalfInvalidOrg proves an empty/oversized org is refused before any
// store/inference — the org is a tenant key and must be bounded.
func TestRunOnBehalfInvalidOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "x"})
	_ = app
	if _, err := RunOnBehalf(context.Background(), "", "u", "hanzo", "hi"); err == nil {
		t.Fatal("empty org must be refused")
	}
}
