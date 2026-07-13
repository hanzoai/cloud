package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/money"
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
	id, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000)})
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if id == "" {
		t.Fatal("deposit returned empty entry id")
	}
	mustBalance(t, f, "acme", "acme", 1000)

	// Debit 300 of usage → 700.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), RequestID: "r1"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 700)

	// Replay the same RequestID → idempotent, still 700 (debited at most once).
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), RequestID: "r1"}); err != nil {
		t.Fatalf("usage replay: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 700)

	// A per-user subject is an isolated wallet WITHIN the same file.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500)}); err != nil {
		t.Fatalf("deposit bob: %v", err)
	}
	mustBalance(t, f, "acme", "acme/bob", 500)
	mustBalance(t, f, "acme", "acme", 700) // the org pool is untouched by bob's wallet
}

// TestDepositRefIdempotent pins DepositInput.Ref idempotency: two deposits with the SAME
// non-empty Ref credit the wallet ONCE (the second replays the first entry id), while
// empty-Ref deposits stay additive (each stacks).
func TestDepositRefIdempotent(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()

	// Same non-empty Ref → credited once.
	id1, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "settle-1"})
	if err != nil {
		t.Fatalf("deposit ref #1: %v", err)
	}
	id2, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "settle-1"})
	if err != nil {
		t.Fatalf("deposit ref #2: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("deposit ref #2 id = %q; want the first entry id %q (idempotent replay)", id2, id1)
	}
	mustBalance(t, f, "acme", "acme", 1000) // ONE credit, not 2000.

	// Empty Ref stays additive: two fresh-ref deposits stack.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500)}); err != nil {
		t.Fatalf("deposit additive #1: %v", err)
	}
	if _, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500)}); err != nil {
		t.Fatalf("deposit additive #2: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 2000) // 1000 + 500 + 500.
}

// TestMigrateOrgIdempotent proves the commerce→finance backfill is exactly-once: running
// MigrateOrg twice for the same org credits the pooled wallet ONE time (the fixed
// "backfill:<org>" ref dedupes the second run), and a non-positive balance is skipped.
func TestMigrateOrgIdempotent(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()
	Publish(f)
	defer Publish(nil)

	// First backfill lands the balance.
	id1, err := MigrateOrg(ctx, "acme", 2500)
	if err != nil {
		t.Fatalf("migrate #1: %v", err)
	}
	if id1 == "" {
		t.Fatal("migrate #1 returned empty entry id")
	}
	mustBalance(t, f, "acme", "acme", 2500)

	// Re-running the cutover is a no-op: same fixed ref → credited AT MOST ONCE.
	id2, err := MigrateOrg(ctx, "acme", 2500)
	if err != nil {
		t.Fatalf("migrate #2: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("migrate #2 id = %q; want the first entry id %q (idempotent replay)", id2, id1)
	}
	mustBalance(t, f, "acme", "acme", 2500) // still ONE credit, not 5000.

	// A non-positive balance is skipped: nothing to carry, empty id, no posting.
	id3, err := MigrateOrg(ctx, "empty", 0)
	if err != nil {
		t.Fatalf("migrate zero: %v", err)
	}
	if id3 != "" {
		t.Fatalf("migrate zero id = %q; want \"\" (skipped)", id3)
	}
	mustBalance(t, f, "empty", "empty", 0)
}

func mustBalance(t *testing.T, f *ledgerFinance, org, subject string, want int64) {
	t.Helper()
	got, err := f.Balance(context.Background(), org, subject, "usd", false)
	if err != nil {
		t.Fatalf("balance(%s,%s): %v", org, subject, err)
	}
	if got.Cents() != want {
		t.Fatalf("balance(%s,%s) = %d; want %d", org, subject, got.Cents(), want)
	}
}
