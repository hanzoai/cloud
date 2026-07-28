package link

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRoutedUsageSums(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := Account{"openai", "work"}
	r1 := Result{Model: "gpt-4o", PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
	r2 := Result{Model: "gpt-4o", PromptTokens: 5, CompletionTokens: 15, TotalTokens: 20}
	if err := s.AddRouted(ctx, "acme", "alice", a, KindAPIKey, r1, 100, 1); err != nil {
		t.Fatalf("add1: %v", err)
	}
	if err := s.AddRouted(ctx, "acme", "alice", a, KindAPIKey, r2, 50, 2); err != nil {
		t.Fatalf("add2: %v", err)
	}
	rows, err := s.RoutedTotals(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 account row, got %d", len(rows))
	}
	got := rows[0]
	if got.Requests != 2 || got.TotalTokens != 50 || got.PromptTokens != 15 || got.CompletionTokens != 35 || got.CostCents != 150 {
		t.Fatalf("sums wrong: %+v", got)
	}
	if got.Provider != "openai" || got.Account != "work" || got.Billing != BillingCommerce {
		t.Fatalf("attribution wrong: %+v", got)
	}
}

func TestRoutedTotalsIsolatedByTenant(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := Account{"openai", "work"}
	// Two tenants add usage for the coincidentally-same account label.
	_ = s.AddRouted(ctx, "acme", "alice", a, KindAPIKey, Result{TotalTokens: 100}, 10, 1)
	_ = s.AddRouted(ctx, "victimco", "carol", a, KindAPIKey, Result{TotalTokens: 999}, 99, 1)

	rows, _ := s.RoutedTotals(ctx, "acme", "alice")
	if len(rows) != 1 || rows[0].TotalTokens != 100 {
		t.Fatalf("acme must see only its own 100 tokens, got %+v", rows)
	}
	// A different subject in the same org sees nothing (per-subject scope).
	rows, _ = s.RoutedTotals(ctx, "acme", "mallory")
	if len(rows) != 0 {
		t.Fatalf("a different subject must see no rows, got %+v", rows)
	}
}

func TestRoutedAccountsView(t *testing.T) {
	rows := []RoutedUsage{
		{Provider: "openai", Account: "work", Requests: 3, TotalTokens: 30, PromptTokens: 10, CompletionTokens: 20, CostCents: 6},
		{Provider: "anthropic", Account: "max", Requests: 1, TotalTokens: 5, CostCents: 0},
	}
	v := routedAccountsView(rows)
	if v.Scope != ScopeUser || v.Source != "routed" {
		t.Fatalf("labels wrong: %+v", v)
	}
	if v.Total.Accounts != 2 || v.Total.Requests != 4 || v.Total.TotalTokens != 35 || v.Total.CostCents != 6 {
		t.Fatalf("totals wrong: %+v", v.Total)
	}
	// Empty input yields an empty (never nil) accounts slice, so the JSON is [] not null.
	if got := routedAccountsView(nil); got.Accounts == nil || got.Total.Accounts != 0 {
		t.Fatalf("empty view should be a zero total with [] accounts, got %+v", got)
	}
}
