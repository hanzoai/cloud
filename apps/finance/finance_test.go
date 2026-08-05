package finance

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/money"
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

// TestDepositAlreadyCreditedIsNotAFailure — a deposit that cannot RUN, on money that is
// already in the books, answers with the money and not with an error.
//
// The in-transaction dedup only covers a replay whose transaction reaches its own read.
// A transaction that never gets that far — the one that lost the write to a concurrent
// poster of the same settlement, or whose request context died between the card clearing
// and the post — leaves the caller an error over a ref that IS credited. At a credit door
// that is a 500 on a settled charge (apps/commerce settle.go), and the customer's own
// retry is what has to recover it.
//
// The context here is CANCELLED, which is that state reproducibly rather than by racing:
// the transaction cannot begin at all, and the entry is nonetheless posted.
//
// Mutation proof: make [creditedUnder] answer ("", false) — or give its read the
// caller's own dying context instead of a detached one — and this fails with
// "begin tx: context canceled" on a wallet holding the money.
func TestDepositAlreadyCreditedIsNotAFailure(t *testing.T) {
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()
	in := types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(4200), Ref: "sq_pay_9Xk2"}

	posted, err := f.Deposit(context.Background(), in)
	if err != nil {
		t.Fatalf("the first deposit: %v", err)
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := f.Deposit(dead, in)
	if err != nil {
		t.Fatalf("a deposit of an ALREADY CREDITED ref failed with %v — the caller is told the money "+
			"did not land while the ledger holds it, which at a credit door is a 500 on a settled card", err)
	}
	if got != posted {
		t.Errorf("it answered entry %q, want the entry the money is actually under, %q", got, posted)
	}
	mustBalance(t, f, "acme", "acme", 4200) // still ONE credit.

	// AND IT DOES NOT INVENT ONE. A ref that was never posted has no credit to report, so
	// the failure is still a failure — otherwise the guard would answer success for money
	// that never moved, which is the defect it exists to prevent, inverted.
	fresh := in
	fresh.Ref = "sq_pay_never_posted"
	if _, err := f.Deposit(dead, fresh); err == nil {
		t.Fatal("a deposit that never ran, on a ref nothing credited, answered SUCCESS — a caller " +
			"would be told a balance exists that does not")
	}
	mustBalance(t, f, "acme", "acme", 4200)
}

// TestDepositConcurrentSettlementsOfOneRef — many posters, one settlement, one credit.
//
// Settlement is at-least-once and about to have a second writer (the processor webhook
// replaying a charge the door already posted). Every one of them names the same Ref, so
// every one of them must be told the money is there and the wallet must hold it once.
func TestDepositConcurrentSettlementsOfOneRef(t *testing.T) {
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()
	in := types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(4200), Ref: "sq_pay_9Xk2"}

	const posters = 16
	ids := make([]string, posters)
	errs := make([]error, posters)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range posters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = f.Deposit(context.Background(), in)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("poster %d was refused (%v) over a settlement the ledger credited", i, err)
		}
		if ids[i] != ids[0] {
			t.Errorf("poster %d answered entry %q, poster 0 answered %q — one settlement, two entries",
				i, ids[i], ids[0])
		}
	}
	mustBalance(t, f, "acme", "acme", 4200)
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

// TestBalanceReadErrorSurfaces pins the money invariant that a REAL balance-read
// failure is surfaced as an error, NEVER rendered as a genuine $0 ("unknown is not
// broke"). Regression guard for the swallowed store.Balance error, which showed a
// funded customer $0 and made the prepaid AI gate refuse a funded org.
func TestBalanceReadErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()

	// Fund acme so the store + a real balance row exist; confirm the happy read.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000)}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 5000)

	// Force a REAL read failure: close the underlying ledger DB out from under the
	// cached store, so the next Balance read errors (closed DB) rather than a genuine
	// zero. This is exactly the DB-error / corrupt-row class balanceOf returns.
	store, err := f.storeFor("acme", false)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	bal, err := f.Balance(ctx, "acme", "acme", "usd", false)
	if err == nil {
		t.Fatalf("a real balance-read failure must surface an error, got (%s, nil) — unknown must never render as $0", bal)
	}
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
