package link

import (
	"context"
	"testing"
)

// TestMeterBillsByAccountKind proves BillingMode governs the charge: an api-key
// account records the priced charge (visible as cost_cents on the routed counter),
// a subscription account records ZERO charge (its plan pays the provider directly) —
// so a routed call is never double-charged. A nil billing client keeps the test off
// commerce; the DECISION is observed through the counter's recorded cost.
func TestMeterBillsByAccountKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	price := func(Result) int64 { return 100 } // a flat 100¢ platform fee per call
	m := NewMeter(s, nil, price, nil)

	res := Result{Model: "gpt-4o", PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
	m.RecordRouted(ctx, "acme", "alice", Account{"openai", "key"}, KindAPIKey, res)
	m.RecordRouted(ctx, "acme", "alice", Account{"anthropic", "max"}, KindSubscription, res)

	rows, err := s.RoutedTotals(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	byAcct := map[string]RoutedUsage{}
	for _, r := range rows {
		byAcct[r.Provider+":"+r.Account] = r
	}
	if got := byAcct["openai:key"]; got.CostCents != 100 || got.Billing != BillingCommerce {
		t.Fatalf("api-key account should charge 100c via commerce: %+v", got)
	}
	if got := byAcct["anthropic:max"]; got.CostCents != 0 || got.Billing != BillingPlan {
		t.Fatalf("subscription account must not be charged (plan pays): %+v", got)
	}
	// Both are metered for usage regardless of billing mode.
	if len(rows) != 2 {
		t.Fatalf("both accounts should be metered for usage, got %d rows", len(rows))
	}
}

// TestMeterZeroPriceDefault proves the honest default: absent an operator fee, an
// api-key routed call still records usage but invents no charge.
func TestMeterZeroPriceDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	m := NewMeter(s, nil, nil, nil) // nil price → ZeroPrice
	m.RecordRouted(ctx, "acme", "alice", Account{"openai", "key"}, KindAPIKey, Result{TotalTokens: 42})
	rows, _ := s.RoutedTotals(ctx, "acme", "alice")
	if len(rows) != 1 || rows[0].TotalTokens != 42 || rows[0].CostCents != 0 {
		t.Fatalf("zero-price default should meter usage with no charge: %+v", rows)
	}
}
