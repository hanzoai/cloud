package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// The money the backfill is there to save: ONE act, straddling the deploy, debited ONCE.
//
// A usage ref is unique WITHIN THE WALLET it debits — the entry carries that wallet as its
// program. Entries written before that scope existed carry no program at all, and the
// probe now asks under the wallet, so it does not see them. Any surface whose ref is
// STABLE across the deploy therefore posts a SECOND debit for work already paid for: the
// customer is charged twice and the first charge is the evidence that they should not
// have been.
//
// The pre-deploy row here is written the way it was written then — the two real legs,
// an EMPTY program — through the same adapter production uses. Reopening the file is what runs
// the migration, exactly as a pod restart on the new image does.
//
// MUTATION PROOF: skip the backfill (drop the s.backfillUsageProgram() call from
// sqlstore.migrate) and the wallet ends at 90¢ instead of 95¢ — the same act billed
// twice, which is the shipped behaviour this test refuses.
func TestBackfilledProgramStopsTheSecondDebit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	const stable = "domain:renew:acme.ai" // a ref a caller holds across the deploy
	before := New(dir)
	if _, err := before.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(100),
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	writeLegacyUsage(t, before, "acme", "acme", stable, money.FromCents(5))
	mustBalance(t, before, "acme", "acme", 95)
	if err := before.Close(); err != nil {
		t.Fatalf("close pre-deploy client: %v", err)
	}

	// THE DEPLOY: a new process opens the same file, so the migration — and the backfill
	// — runs, and the act is re-recorded under the ref its caller still holds.
	after := New(dir)
	defer func() { _ = after.Close() }()
	if err := after.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(5), Ref: stable,
	}); err != nil {
		t.Fatalf("post-deploy usage: %v", err)
	}
	mustBalance(t, after, "acme", "acme", 95) // ONE debit for ONE act, not two.
}

// The other half, and the half a backfill is most likely to break: repairing the key must
// not make two DIFFERENT acts look like one. Two pre-deploy debits out of the SAME wallet
// under DIFFERENT refs stay two, and a fresh act after the deploy still bills.
func TestBackfillKeepsDistinctActsDistinct(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	before := New(dir)
	if _, err := before.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme/bob", Amount: money.FromCents(100),
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	writeLegacyUsage(t, before, "acme", "acme/bob", "act_a", money.FromCents(5))
	writeLegacyUsage(t, before, "acme", "acme/bob", "act_b", money.FromCents(7))
	mustBalance(t, before, "acme", "acme/bob", 88)
	if err := before.Close(); err != nil {
		t.Fatalf("close pre-deploy client: %v", err)
	}

	after := New(dir)
	defer func() { _ = after.Close() }()
	// Both old acts replay (no further money moves) and a NEW act bills.
	for _, tc := range []struct {
		ref   string
		cents int64
	}{{"act_a", 5}, {"act_b", 7}} {
		if err := after.RecordUsage(ctx, types.UsageInput{
			Org: "acme", Subject: "acme/bob", Amount: money.FromCents(tc.cents), Ref: tc.ref,
		}); err != nil {
			t.Fatalf("replay %s: %v", tc.ref, err)
		}
	}
	mustBalance(t, after, "acme", "acme/bob", 88)

	if err := after.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme/bob", Amount: money.FromCents(3), Ref: "act_c",
	}); err != nil {
		t.Fatalf("fresh act: %v", err)
	}
	mustBalance(t, after, "acme", "acme/bob", 85)
}

// A repaired ref belongs to the wallet that PAID it, and to no other wallet in the file.
//
// This is the bug the wallet scope was introduced for, seen from the other side: before
// the deploy one org-wide namespace held every ref, so the FIRST subject to take one owned
// it for everyone and the second subject's call debited nobody. (The fixture proves it —
// two pre-deploy rows under one ref cannot even be written: the unique key refuses the
// second.) The backfill must hand the repaired key to the payer alone, so the subject who
// was swallowed then is billed now.
func TestARepairedRefBelongsToThePayerAlone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	const shared = "act_shared"
	before := New(dir)
	for _, subject := range []string{"acme", "acme/bob"} {
		if _, err := before.Deposit(ctx, types.DepositInput{
			Org: "acme", Subject: subject, Amount: money.FromCents(100),
		}); err != nil {
			t.Fatalf("seed %s: %v", subject, err)
		}
	}
	// The org pool paid for it; bob's identical call is the one the old key swallowed.
	writeLegacyUsage(t, before, "acme", "acme", shared, money.FromCents(5))
	if err := before.Close(); err != nil {
		t.Fatalf("close pre-deploy client: %v", err)
	}

	after := New(dir)
	defer func() { _ = after.Close() }()

	// The payer replays into its own repaired entry: no second debit.
	if err := after.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(5), Ref: shared,
	}); err != nil {
		t.Fatalf("payer replay: %v", err)
	}
	mustBalance(t, after, "acme", "acme", 95)

	// Bob's wallet never paid it, so bob's call is a debit and not a replay.
	if err := after.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme/bob", Amount: money.FromCents(5), Ref: shared,
	}); err != nil {
		t.Fatalf("second subject under the same ref: %v", err)
	}
	mustBalance(t, after, "acme", "acme/bob", 95)
	mustBalance(t, after, "acme", "acme", 95) // and the payer is not charged again for it
}

// writeLegacyUsage posts a usage debit the way it was posted BEFORE a usage ref was scoped
// to its wallet: the same two legs, and an EMPTY program. Only that emptiness is fabricated —
// the entry goes in through the adapter production writes with, so the row is the row.
func writeLegacyUsage(t *testing.T, f *ledgerFinance, org, subject, ref string, amount money.Amount) {
	t.Helper()
	store, err := f.storeFor(org, false)
	if err != nil {
		t.Fatalf("open %s ledger: %v", org, err)
	}
	id := mint.ID("use")
	if err := store.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(
			ledger.JournalEntry{ID: id, Kind: string(KindUsage), Program: "", Ref: ref, Amount: amount},
			[]ledger.Posting{
				{Account: walletAcct(subject), Amount: amount.Neg()},
				{Account: acctRevenue, Amount: amount},
			})
	}); err != nil {
		t.Fatalf("write pre-deploy usage %s: %v", ref, err)
	}
}
