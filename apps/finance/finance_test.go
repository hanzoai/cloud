package finance

import (
	"context"
	"errors"
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
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "r1"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 700)

	// Replay the same Ref → idempotent, still 700 (debited at most once).
	if err := f.RecordUsage(ctx, types.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "r1"}); err != nil {
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

// TestDepositRefIsOnePaymentAndNotJustOneKey — the swallow.
//
// The idempotency key is (kind, program, ref) and carries NEITHER subject NOR amount, so
// two DIFFERENT payments naming one ref in one org's books collide. The replay branch
// answered the second of them with the FIRST one's entry id: alice's $1 posted, bob's
// $500 was told SUCCESS, bob's wallet stayed at zero, and nothing anywhere said a payment
// had been dropped. It has never fired only because the settlement ref is a Square payment
// id and no two payments share one — a replayed webhook, a backfill, or the credit RPC
// choosing a ref of its own is all it takes.
//
// A ref hit is a REPLAY only when it is the same money to the same wallet. Anything else
// is a different payment wearing this one's key, and the only honest answer is an error:
// crediting anyway breaks the exactly-once the ref exists to give, and handing back the
// other payment's entry IS the swallow.
//
// Mutation proof: drop the (subject, amount) comparison from [depositByRef] — answer
// e.ID for any ref hit, the shipped behaviour — and all three conflict rows answer
// SUCCESS with alice's entry id over a wallet holding nothing. The first row is the
// other direction and equally load-bearing: refuse a genuine replay and exactly-once
// becomes at-most-zero, so a retrying settlement 500s on a card that cleared.
func TestDepositRefIsOnePaymentAndNotJustOneKey(t *testing.T) {
	// alice's payment lands first and owns the ref.
	const org, ref = "acme", "SHARED"
	alice := types.DepositInput{Org: org, Subject: "acme/alice", Amount: money.FromCents(100), Ref: ref}
	under := func(subject string, cents int64) types.DepositInput {
		return types.DepositInput{Org: org, Subject: subject, Amount: money.FromCents(cents), Ref: ref}
	}

	for _, tc := range []struct {
		name string
		// second is the deposit posted after alice's, naming her ref.
		second types.DepositInput
		// replay says it IS alice's payment again: same money, same wallet.
		replay bool
	}{
		{"the same money to the same wallet IS the replay the ref exists for", under("acme/alice", 100), true},
		{"a different PAYER on one ref", under("acme/bob", 100), false},
		{"a different AMOUNT on one ref", under("acme/alice", 50000), false},
		{"a different payer AND amount — the measured case", under("acme/bob", 50000), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := New(t.TempDir())
			defer func() { _ = f.Close() }()

			posted, err := f.Deposit(ctx, alice)
			if err != nil {
				t.Fatalf("alice's deposit: %v", err)
			}
			mustBalance(t, f, org, alice.Subject, 100)

			got, err := f.Deposit(ctx, tc.second)

			if tc.replay {
				if err != nil {
					t.Fatalf("a genuine replay was refused (%v) — exactly-once has become at-most-zero "+
						"and a retried settlement 500s on a card that cleared", err)
				}
				if got != posted {
					t.Errorf("the replay answered entry %q, want the original %q", got, posted)
				}
				mustBalance(t, f, org, alice.Subject, 100) // ONE credit, not two.
				return
			}

			if err == nil {
				t.Fatalf("%s of %s under ANOTHER payment's ref answered SUCCESS with entry %q — the "+
					"caller is told money landed that this ledger never credited",
					tc.second.Subject, tc.second.Amount, got)
			}
			if !errors.Is(err, ErrRefTaken) {
				t.Errorf("it failed with %v, want a conflict on the ref (%v)", err, ErrRefTaken)
			}
			if got != "" {
				t.Errorf("the conflict answered entry %q, want none — that id is the OTHER payment's", got)
			}
			// AND NEITHER WALLET IS WRONG: alice keeps her credit, and the payment that was
			// refused credited nobody.
			mustBalance(t, f, org, alice.Subject, 100)
			if tc.second.Subject != alice.Subject {
				mustBalance(t, f, org, tc.second.Subject, 0)
			}
		})
	}
}

// TestDepositRecoveryIsNotSomebodyElsesCredit — the same rule on the recovery path.
//
// [creditedUnder] exists so a deposit that could not RUN, over money already in the books,
// answers with the money rather than with a 500 on a settled card
// ([TestDepositAlreadyCreditedIsNotAFailure]). It asked only whether the REF was posted,
// so a transaction that died over a ref belonging to a DIFFERENT payment was handed that
// payment's entry and reported success — the same swallow as the replay branch, on the one
// path whose whole job is not to lie about where money is.
//
// The context is CANCELLED, so the transaction cannot begin and the answer comes from the
// recovery read and from nothing else.
//
// Mutation proof: give creditedUnder back its ref-only lookup and this answers alice's
// entry id for bob's $500 over a wallet holding nothing.
func TestDepositRecoveryIsNotSomebodyElsesCredit(t *testing.T) {
	const org, ref = "acme", "SHARED"
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()

	alice := types.DepositInput{Org: org, Subject: "acme/alice", Amount: money.FromCents(100), Ref: ref}
	if _, err := f.Deposit(context.Background(), alice); err != nil {
		t.Fatalf("alice's deposit: %v", err)
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	bob := types.DepositInput{Org: org, Subject: "acme/bob", Amount: money.FromCents(50000), Ref: ref}
	got, err := f.Deposit(dead, bob)
	if err == nil {
		t.Fatalf("bob's $500 answered SUCCESS with entry %q over alice's $1 — a payment was swallowed "+
			"and its payer told it landed", got)
	}
	// It names the REF as the reason, not the dead context: a retry under this ref can
	// never succeed, so the caller must be told to stop retrying it rather than to loop.
	if !errors.Is(err, ErrRefTaken) {
		t.Errorf("it failed with %v, want a conflict on the ref (%v)", err, ErrRefTaken)
	}
	if got != "" {
		t.Errorf("it answered entry %q, want none", got)
	}
	mustBalance(t, f, org, alice.Subject, 100)
	mustBalance(t, f, org, bob.Subject, 0)
}

// TestDepositAlreadyCreditedIsNotAFailure — a deposit that cannot RUN, on money that is
// already in the books, answers with the money and not with an error.
//
// The in-transaction dedup only covers a replay whose transaction reaches its own read.
// A transaction that never gets that far — the one that lost the write to a concurrent
// poster of the same settlement, or whose request context died between the card clearing
// and the post — leaves the caller an error over a ref that IS credited. At a credit
// endpoint that is a 500 on a settled charge (apps/commerce settle.go), and the
// customer's own retry is what has to recover it.
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
			"did not land while the ledger holds it, which at a credit endpoint is a 500 on a settled card", err)
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
// replaying a charge the endpoint already posted). Every one of them names the same
// Ref, so every one of them must be told the money is there and the wallet must hold
// it once.
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

// TestMigrateOrgRerunAfterTheBalanceMoved — the exactly-once promise over the one input
// that MOVES.
//
// [MigrateOrg] documents a re-run as a no-op that answers the original entry, and the
// caller is an operator-facing admin endpoint that says "safe to retry". Both are only
// true while the balance being carried is the same number, because the deposit's
// idempotency answers a replay with the first entry only when the ref names the same
// (subject, AMOUNT) — and a legacy balance is a moving figure by construction: the
// customer spends, tops up, is granted credit between the first run and the second.
//
// So the honest re-run — the retry the doc invites, over a balance that changed in the
// meantime — was answered with a REF CONFLICT. The cutover looked failed, its receipt was
// an error, and an operator reading it has no way to tell "already carried" from "the
// carry is broken".
//
// It is asserted AFTER A SPEND as well, because that is the state a real second run is
// in: the wallet no longer holds the carried figure, and the re-run must still move
// nothing rather than topping it back up.
//
// Mutation proof: drop the [ErrRefTaken] read-back from [MigrateOrg] (return the deposit's
// error) and the re-run fails on the error; answer the conflict with a fresh deposit
// instead and it fails on the balance, which is the money half.
func TestMigrateOrgRerunAfterTheBalanceMoved(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()
	Publish(f)
	defer Publish(nil)

	first, err := MigrateOrg(ctx, "acme", 2500)
	if err != nil {
		t.Fatalf("the cutover: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 2500)

	// The org spends part of what was carried, so the wallet no longer holds the figure
	// the entry was posted for.
	if err := f.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "spend-1",
	}); err != nil {
		t.Fatalf("spend: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 1500)

	// The cutover is run again and commerce's legacy balance is a DIFFERENT number now.
	again, err := MigrateOrg(ctx, "acme", 1500)
	if err != nil {
		t.Fatalf("re-running the cutover over a MOVED balance answered %v — the documented no-op is an "+
			"error, so a retried operator call reads as a broken cutover and the admin endpoint reports "+
			"a failure over money that is already carried", err)
	}
	if again != first {
		t.Fatalf("the re-run answered entry %q, want the original %q — a carry that answers a new "+
			"identity every time is not exactly-once, whatever it moved", again, first)
	}
	mustBalance(t, f, "acme", "acme", 1500) // no second carry: the wallet is untouched.

	// And it is stable: a THIRD run, over a third figure, is the same answer again.
	third, err := MigrateOrg(ctx, "acme", 90000)
	if err != nil {
		t.Fatalf("the third run: %v", err)
	}
	if third != first {
		t.Fatalf("the third run answered entry %q, want the original %q", third, first)
	}
	mustBalance(t, f, "acme", "acme", 1500)
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
