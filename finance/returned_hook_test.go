package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// RED729C-F1 (LOW). SetLimit announces unconditionally after the settle, so LOWERING a credit line (or writing the same
// one) wakes every run parked for want of funds in the org: a pass over the org's parks, an IAM read and three authority
// reads per park, and an attempt number advanced per park -- for a change that cannot clear anything. The hook's own doc
// says "a credit line raised"; LLM.md says "puts money in a wallet".
func TestRed729cALoweredCreditLineTellsNobody(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	h := hook(t)
	if _, err := f.SetLimit(ctx, "acme", false, money.FromCents(10000)); err != nil {
		t.Fatal(err)
	}
	h.told(t, 1, "a credit line raised")
	if _, err := f.SetLimit(ctx, "acme", false, money.FromCents(5000)); err != nil {
		t.Fatal(err)
	}
	h.told(t, 1, "RED: a credit line lowered")
	if _, err := f.SetLimit(ctx, "acme", false, money.FromCents(5000)); err != nil {
		t.Fatal(err)
	}
	h.told(t, 1, "RED: the same credit line written again")
}

// RED729C-F2 (LOW). Return posts and settles only on the call that posted it; the replay of a Return whose first
// settle failed answers "returned already" (zero) and neither settles nor tells. Deposit, Refund and Restore all tell on the
// replay (the commit brief asks for it: "fires on a replay after a failed settle"); Return does not, so a hold given back
// whose first acknowledgement failed never tells the org's parked runs.
func TestRed729cAReturnRetriedAfterAFailedSettleTells(t *testing.T) {
	ctx := context.Background()
	books := &shaky{Ledgers: Local(t.TempDir(), testMaster)}
	books.failed.Store(true) // the deposit below settles
	f := New(books)
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "promo"}); err != nil {
		t.Fatal(err)
	}
	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(700), Ref: "x402_9"}
	if _, _, err := f.Hold(ctx, job); err != nil {
		t.Fatal(err)
	}
	h := hook(t)
	books.failed.Store(false) // the next acknowledgement fails once
	if _, err := f.Return(ctx, job); err == nil {
		t.Fatal("the first return should have failed to settle")
	}
	h.told(t, 0, "a return that could not settle")
	if _, err := f.Return(ctx, job); err != nil {
		t.Fatal(err)
	}
	h.told(t, 1, "RED: the retried return (the money is back in the wallet, acknowledged by no call)")
}
