package finance

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// TestUsageRefIsScopedToTheWallet — the swallow, on the usage side.
//
// THE BUG. The ledger keys an entry by (kind, program, ref) and usage passed program
// EMPTY, so one namespace per ORG held every subject's refs. The first subject to take a
// ref owned it for everyone in the org: alice's call posted, bob's call carrying the same
// ref was answered with alice's entry, bob's wallet never moved, and nothing said a
// charge had been dropped. Two people in one org sharing a ref — which the metered path
// handed out freely, since the ref was the caller's X-Request-Id — meant one of them
// worked for free at the other's expense.
//
// THE PROPERTY. A usage ref is unique WITHIN THE WALLET IT DEBITS, and the wallet is
// named by the same rule the posting uses ([walletAcct]), so the key and the money can
// never address two different accounts. Alice and bob may each hold a ref spelled the
// same; each pays their own.
//
// MUTATION PROOF: drop the subject from the key —
//
//	func usageProgram(subject string) string { return "" }   // the shipped behaviour
//
// and bob's wallet reads 100¢: his 30¢ call found alice's entry, returned it, and
// debited nobody.
func TestUsageRefIsScopedToTheWallet(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir()))
	defer func() { _ = f.Close() }()

	// Two people, ONE org — so ONE ledger file, which is exactly where the refs used to
	// collide.
	for _, who := range []string{"acme/alice", "acme/bob"} {
		if _, err := f.Deposit(ctx, types.DepositInput{
			Org: "acme", Subject: who, Amount: money.FromCents(100),
		}); err != nil {
			t.Fatalf("seed %s: %v", who, err)
		}
	}

	// The SAME ref, the same amount — the collision, arranged deliberately.
	const shared = "deadbeefdeadbeefdeadbeefdeadbeef"
	for _, who := range []string{"acme/alice", "acme/bob"} {
		if err := f.RecordUsage(ctx, types.UsageInput{
			Org: "acme", Subject: who, Amount: money.FromCents(30), Model: "zen-1", Ref: shared,
		}); err != nil {
			t.Fatalf("usage %s: %v", who, err)
		}
	}

	// Each paid their own.
	mustBalance(t, f, "acme", "acme/alice", 70)
	mustBalance(t, f, "acme", "acme/bob", 70)
}

// TestUsageRefIsOneChargeAndNotJustOneKey pins the other half of a named act: within one
// wallet, a ref names ONE charge.
//
// A caller that may NAME the act — the GPU charge is the one such surface — is a caller
// that can reuse a name. Answering the second charge with the first one's entry hands
// back work nobody paid for, so a ref hit whose amount differs is refused
// ([ErrRefReused]) rather than deduped. Re-sending the SAME charge under its own name
// still moves the money once: that is the retry safety the key exists for.
//
// MUTATION PROOF: drop the amount comparison from [usageByRef] — return e.ID for any ref
// hit — and the $5.00 charge under a name already spent on $0.30 is answered "ok" while
// the wallet keeps its $5.00.
func TestUsageRefIsOneChargeAndNotJustOneKey(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir()))
	defer func() { _ = f.Close() }()

	if _, err := f.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(1000),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const key = "launch-abc"
	charge := func(cents int64) error {
		return f.RecordUsage(ctx, types.UsageInput{
			Org: "acme", Subject: "acme", Amount: money.FromCents(cents), Model: "gpu", Ref: key,
		})
	}

	if err := charge(30); err != nil {
		t.Fatalf("first charge: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 970)

	// The SAME charge again — a retry. Exactly-once: the wallet does not move.
	if err := charge(30); err != nil {
		t.Fatalf("retry of the same charge: %v", err)
	}
	mustBalance(t, f, "acme", "acme", 970)

	// A DIFFERENT charge wearing that name. Refused, and no money moves either way.
	err := charge(500)
	if !errors.Is(err, ErrRefReused) {
		t.Fatalf("a $5.00 charge under a name already spent on $0.30 failed with %v; want %v", err, ErrRefReused)
	}
	mustBalance(t, f, "acme", "acme", 970)
}

// TestUsageWithoutARefStandsAlone pins the additive default: no name, no dedup. Every
// metered call takes this path — the meter mints a name per act, and an act with none at
// all still bills exactly once, never zero and never twice.
func TestUsageWithoutARefStandsAlone(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir()))
	defer func() { _ = f.Close() }()

	if _, err := f.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(100),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for range 10 {
		if err := f.RecordUsage(ctx, types.UsageInput{
			Org: "acme", Subject: "acme", Amount: money.FromCents(5), Model: "zen-1",
		}); err != nil {
			t.Fatalf("usage: %v", err)
		}
	}
	mustBalance(t, f, "acme", "acme", 50) // ten calls, ten debits
}
