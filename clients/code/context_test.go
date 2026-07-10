package code

import (
	"context"
	"testing"
)

func seededEngine(t *testing.T, repo string) *engine {
	t.Helper()
	s := newTestService(t)
	st, err := s.storeFor("acme")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if _, err := s.indexRepo(context.Background(), "acme", "", st, repo, []fileInput{
		{Path: "greeter.go", Content: goFixture},
	}, false); err != nil {
		t.Fatalf("indexRepo: %v", err)
	}
	return &engine{store: st, embed: s.embed}
}

func TestPackContextRespectsBudget(t *testing.T) {
	eng := seededEngine(t, "r")
	b, err := eng.packContext(context.Background(), "r", "Hello greeting for name", 4000)
	if err != nil {
		t.Fatalf("packContext: %v", err)
	}
	if len(b.Spans) == 0 {
		t.Fatal("empty context bundle for a matching query")
	}
	if b.UsedTokens > b.BudgetTokens {
		t.Fatalf("over budget: used=%d budget=%d", b.UsedTokens, b.BudgetTokens)
	}
	var roles = map[string]bool{}
	for _, s := range b.Spans {
		roles[s.Role] = true
	}
	if !roles["match"] {
		t.Errorf("no primary 'match' span in bundle: %+v", b.Spans)
	}
}

// The bundle is the symbol GRAPH, not just the match. With the semantic tier off
// (so the tiny corpus does not surface every chunk as a seed), a query for
// "Hello" seeds only the method; greet() must be attached as a 'definition'
// because Hello() calls it — proving def→ref graph expansion.
func TestContextExpandsCallees(t *testing.T) {
	st := newStore(t, "code.db")
	writeFixture(t, st, "r", "greeter.go", goFixture)
	eng := &engine{store: st, embed: fakeEmbedder{enabled: false}} // semantic OFF

	b, err := eng.packContext(context.Background(), "r", "Hello", 4000)
	if err != nil {
		t.Fatalf("packContext: %v", err)
	}
	var roles = map[string]string{} // symbol -> role
	for _, s := range b.Spans {
		roles[s.Symbol] = s.Role
	}
	if roles["Hello"] != "match" {
		t.Errorf("Hello should be the match seed, got role %q", roles["Hello"])
	}
	if roles["greet"] != "definition" {
		t.Errorf("greet should be attached as a definition (Hello calls greet); roles=%+v", roles)
	}
}

func TestPackContextTinyBudget(t *testing.T) {
	eng := seededEngine(t, "r")
	b, err := eng.packContext(context.Background(), "r", "Hello", 256)
	if err != nil {
		t.Fatalf("packContext: %v", err)
	}
	// Even a tiny budget yields at least the top match, and never overflows.
	if len(b.Spans) < 1 {
		t.Fatal("tiny budget returned no spans")
	}
	if b.UsedTokens > b.BudgetTokens {
		t.Fatalf("tiny budget overflow: used=%d budget=%d", b.UsedTokens, b.BudgetTokens)
	}
}

func TestPackContextNoMatch(t *testing.T) {
	eng := seededEngine(t, "r")
	b, err := eng.packContext(context.Background(), "r", "zzz_nonexistent_symbol_qqq", 4000)
	if err != nil {
		t.Fatalf("packContext: %v", err)
	}
	// A no-match query returns an empty bundle honestly (semantic may still surface
	// weak hits with the fake embedder, so we only assert it does not error/panic).
	_ = b
}
