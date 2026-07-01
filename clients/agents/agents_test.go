package agents

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mk(org, name string) Agent {
	now := time.Now().Unix()
	return Agent{
		ID: org + "-" + name + "-id", Org: org, Name: name, Model: "gpt-4o-mini",
		Instructions: "You are " + name, Description: "d", Tools: []string{"http"},
		Status: "ready", CreatedAt: now, UpdatedAt: now,
	}
}

// fakeAI is a deterministic AIClient for exercising executeRun without a real
// gateway — proves the compose + record contract, not the model.
type fakeAI struct {
	gotModel  string
	gotPrompt string
	content   string
	err       error
}

func (f *fakeAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	f.gotModel = req.Model
	f.gotPrompt = req.Prompt
	if f.err != nil {
		return nil, f.err
	}
	return &types.ChatResponse{Content: f.content}, nil
}

func TestCreateGetListDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, mk("maxpower", "helper")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, mk("maxpower", "helper")); err != errConflict {
		t.Fatalf("duplicate create should conflict, got %v", err)
	}
	a, err := s.Get(ctx, "maxpower", "helper")
	if err != nil || a.Model != "gpt-4o-mini" {
		t.Fatalf("get: %v model=%q", err, a.Model)
	}
	list, _ := s.List(ctx, "maxpower")
	if len(list) != 1 {
		t.Fatalf("want 1 agent, got %d", len(list))
	}
	deleted, err := s.Delete(ctx, "maxpower", "helper")
	if err != nil || !deleted {
		t.Fatalf("delete: %v deleted=%v", err, deleted)
	}
	if _, err := s.Get(ctx, "maxpower", "helper"); err != errNotFound {
		t.Fatalf("want notfound after delete, got %v", err)
	}
}

// TestTenantIsolation: one org cannot read, run-log, or delete another's agents.
func TestTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, mk("maxpower", "shared")); err != nil {
		t.Fatalf("seed maxpower: %v", err)
	}
	if err := s.Create(ctx, mk("acme", "shared")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	// Record a run for maxpower's agent only.
	if err := s.InsertRun(ctx, Run{ID: "r1", Org: "maxpower", AgentName: "shared", Status: "ok", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	mpRuns, _ := s.ListRuns(ctx, "maxpower", "shared", 50)
	if len(mpRuns) != 1 {
		t.Fatalf("maxpower should have 1 run, got %d", len(mpRuns))
	}
	acRuns, _ := s.ListRuns(ctx, "acme", "shared", 50)
	if len(acRuns) != 0 {
		t.Fatalf("acme must NOT see maxpower's runs, got %d", len(acRuns))
	}
	if n, _ := s.CountRuns(ctx, "acme", "shared"); n != 0 {
		t.Fatalf("acme run count must be 0, got %d", n)
	}

	// acme deleting "shared" must not remove maxpower's agent or its run log.
	if _, err := s.Delete(ctx, "acme", "shared"); err != nil {
		t.Fatalf("acme delete own: %v", err)
	}
	if _, err := s.Get(ctx, "maxpower", "shared"); err != nil {
		t.Fatalf("maxpower agent must survive acme delete: %v", err)
	}
	if n, _ := s.CountRuns(ctx, "maxpower", "shared"); n != 1 {
		t.Fatalf("maxpower run log must survive acme delete, got %d", n)
	}
}

func TestExecuteRunOK(t *testing.T) {
	ai := &fakeAI{content: "hi there"}
	a := mk("maxpower", "greeter")
	a.Instructions = "You are a greeter."
	r := executeRun(context.Background(), ai, "maxpower", a, "say hi")

	if r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	if r.Output != "hi there" {
		t.Fatalf("output should be the model content, got %q", r.Output)
	}
	if ai.gotModel != "gpt-4o-mini" {
		t.Fatalf("run must use the agent's model, got %q", ai.gotModel)
	}
	if ai.gotPrompt != "You are a greeter.\n\nsay hi" {
		t.Fatalf("prompt must compose instructions + input, got %q", ai.gotPrompt)
	}
	if r.Org != "maxpower" || r.AgentName != "greeter" {
		t.Fatalf("run must be scoped to the org+agent, got %+v", r)
	}
}

func TestExecuteRunRecordsError(t *testing.T) {
	ai := &fakeAI{err: errors.New("model unavailable")}
	r := executeRun(context.Background(), ai, "maxpower", mk("maxpower", "x"), "in")
	if r.Status != "error" {
		t.Fatalf("want error status, got %q", r.Status)
	}
	if r.Error != "model unavailable" {
		t.Fatalf("error must be recorded honestly, got %q", r.Error)
	}
	if r.Output != "" {
		t.Fatalf("failed run must not fabricate output, got %q", r.Output)
	}
}
