package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// TestWalletAcct pins the load-bearing subject→account rule: the file is the org
// boundary, so the org pool is "wallet" and a per-user subject "<org>/<user>" is
// "wallet:<user>" (further "/" flattened to ":"), lowercased + trimmed.
func TestWalletAcct(t *testing.T) {
	cases := map[string]string{
		"hanzo":        "wallet",
		"hanzo/z":      "wallet:z",
		"acme":         "wallet",
		"acme/bob":     "wallet:bob",
		"Acme/Bob":     "wallet:bob",
		"hanzo/z/team": "wallet:z:team",
		"hanzo/":       "wallet",
		"":             "wallet",
		"  acme  ":     "wallet",
	}
	for in, want := range cases {
		if got := walletAcct(in); got != want {
			t.Errorf("walletAcct(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestPrepaidWalletLedger drives the money path end to end on a real per-org SQLite file:
// deposit, debit, idempotent replay, and per-user wallet isolation within the one file.
func TestPrepaidWalletLedger(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()

	// Deposit 1000 into acme's org pool.
	id, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Cents: 1000})
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if id == "" {
		t.Fatal("deposit returned empty entry id")
	}
	mustBalance(t, f, "acme", "acme", 1000)

	// Debit 300 of usage → 700.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Cents: 300, RequestID: "r1"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 700)

	// Replay the same RequestID → idempotent, still 700 (debited at most once).
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Cents: 300, RequestID: "r1"}); err != nil {
		t.Fatalf("usage replay: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 700)

	// A per-user subject is an isolated wallet WITHIN the same file.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme/bob", Cents: 500}); err != nil {
		t.Fatalf("deposit bob: %v", err)
	}
	mustBalance(t, f, "acme", "acme/bob", 500)
	mustBalance(t, f, "acme", "acme", 700) // the org pool is untouched by bob's wallet
}

func mustBalance(t *testing.T, f *ledgerFinance, org, subject string, want int64) {
	t.Helper()
	got, err := f.BalanceCents(context.Background(), org, subject, "usd", false)
	if err != nil {
		t.Fatalf("balance(%s,%s): %v", org, subject, err)
	}
	if got != want {
		t.Fatalf("balance(%s,%s) = %d; want %d", org, subject, got, want)
	}
}
