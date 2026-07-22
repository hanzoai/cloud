package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// scriptedAI is a deterministic AIClient that models a throttled gateway: a call
// whose model still has "busy budget" (or when allBusy is set) returns a
// transient ErrUpstreamBusy-tagged error — exactly the shape the real httpAI
// produces for a 429 / "Platform overloaded" — otherwise it returns content. It
// records every call + model so a test can assert the retry count and which model
// finally answered. It does NOT implement types.ModelLister, so create fails open
// (any model is accepted without a catalog round-trip).
type scriptedAI struct {
	mu      sync.Mutex
	calls   int
	models  []string
	busy    map[string]int // model -> remaining transient failures before it succeeds
	allBusy bool           // every call fails transiently (failover can never win)
	content string
}

func (s *scriptedAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.models = append(s.models, req.Model)
	if s.allBusy {
		return nil, fmt.Errorf("429 Platform overloaded: %w", types.ErrUpstreamBusy)
	}
	if n := s.busy[req.Model]; n > 0 {
		s.busy[req.Model] = n - 1
		return nil, fmt.Errorf("429 Platform overloaded: %w", types.ErrUpstreamBusy)
	}
	return &types.ChatResponse{Content: s.content}, nil
}

func (s *scriptedAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (s *scriptedAI) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptedAI) modelsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models...)
}

// TestRunRetriesTransientThenSucceedsBillsOnce is the core reliability contract:
// a gateway that 429s TWICE then answers must yield ONE successful run and EXACTLY
// ONE debit — not a dropped reply (the old single-shot behavior), and not three
// debits (retries must never bill the failed attempts; only the final success
// bills, once).
func TestRunRetriesTransientThenSucceedsBillsOnce(t *testing.T) {
	bs := &billServer{available: 100000}
	ai := &scriptedAI{content: "recovered", busy: map[string]int{"gpt-4o-mini": 2}}
	app := mountBilled(t, bs.start(t), ai)

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run that recovers after 2 retries want 200, got %d (%s)", code, body)
	}
	// The reply must be the recovered content — not dropped.
	var rv runView
	_ = json.Unmarshal(body, &rv)
	if rv.Output != "recovered" {
		t.Fatalf("reply must carry the recovered output, got %q", rv.Output)
	}
	if got := ai.callCount(); got != 3 {
		t.Fatalf("want 3 completion attempts (2 busy + 1 ok), got %d", got)
	}
	// Exactly one debit for the single eventual success — never one-per-attempt.
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a retried-then-succeeded run must debit exactly once, got %d", bs.debits())
	}
	// Give any erroneous extra debit a chance to land; assert it did not.
	if bs.debits() != 1 {
		t.Fatalf("retries must not bill failed attempts: want 1 debit, got %d", bs.debits())
	}
}

// TestRunFailsOverToReliableModelAndBillsIt proves the escalation: the agent's
// own model stays throttled through all its retries, so the run fails over to the
// configured reliable model ("best"), still lands a reply, and bills the model
// ACTUALLY used (best) — not the throttled one it started on.
func TestRunFailsOverToReliableModelAndBillsIt(t *testing.T) {
	bs := &billServer{available: 100000}
	// gpt-4o-mini is busy for MORE than its retry budget (never recovers); "best"
	// answers first try.
	ai := &scriptedAI{content: "from-best", busy: map[string]int{"gpt-4o-mini": 99}}
	app := mountBilled(t, bs.start(t), ai)

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("failover run want 200, got %d (%s)", code, body)
	}
	var rv runView
	_ = json.Unmarshal(body, &rv)
	if rv.Output != "from-best" {
		t.Fatalf("failover reply must come from the reliable model, got %q", rv.Output)
	}
	// Primary exhausted its retries, then exactly one failover attempt on "best".
	if got := ai.callCount(); got != maxAttempts+1 {
		t.Fatalf("want %d attempts (primary exhausted + 1 failover), got %d", maxAttempts+1, got)
	}
	seen := ai.modelsSeen()
	if seen[len(seen)-1] != "best" {
		t.Fatalf("the final attempt must be the failover model, got %q", seen[len(seen)-1])
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a failover run must debit exactly once, got %d", bs.debits())
	}
	_, ubody := bs.lastDebit()
	var u struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(ubody, &u)
	if u.Model != "best" {
		t.Fatalf("failover run must bill the model actually used (best), got %q", u.Model)
	}
}

// TestRunAllAttemptsBusyExhaustedNoDebit proves the failure contract: when EVERY
// attempt on BOTH the agent's model and the failover model is throttled, the run
// records a clean error (502) and NOTHING is billed — a persistent overload never
// costs the tenant, and never fabricates a reply.
func TestRunAllAttemptsBusyExhaustedNoDebit(t *testing.T) {
	bs := &billServer{available: 100000}
	ai := &scriptedAI{allBusy: true}
	app := mountBilled(t, bs.start(t), ai)

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	code, _ := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusBadGateway {
		t.Fatalf("exhausted run want 502, got %d", code)
	}
	// Primary retries (maxAttempts) + failover retries (maxAttempts) were all tried.
	if got := ai.callCount(); got != 2*maxAttempts {
		t.Fatalf("want %d attempts (primary + failover, both exhausted), got %d", 2*maxAttempts, got)
	}
	if waitForDebit(func() bool { return bs.debits() > 0 }) {
		t.Fatalf("a fully-throttled run must NOT be billed, got %d debits", bs.debits())
	}
}
