package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// Red PoC: commerce's prepaid.Refund deletes its billing-draw/billing-split guards so
// "a retry draws afresh", then the retry calls Debit -> Draw under the SAME ref. finance
// finds the original payment entry by (kind, program, ref) and answers it as a replay.
// Nothing moves: the plan opens on the retry and the wallet keeps the refunded money.
func TestARetryAfterARefundPaysAgain(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "sq_1", Cash: true})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "promo"})
	plan := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Ref: "record:acme:team:1790000000"}

	first, posted, err := f.Draw(ctx, plan)
	if err != nil || !posted {
		t.Fatalf("first draw = %q posted=%v err=%v", first, posted, err)
	}
	mustFunds(t, f, "acme", "acme", 0, 3500)

	// openPaid failed -> Refund, guards released.
	if _, err := f.Refund(ctx, plan); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 1000, 5000)

	// The operator's retry: same subject/plan/periodStart -> same drawRef.
	again, posted2, err := f.Draw(ctx, plan)
	if err != nil {
		t.Fatalf("retry draw err=%v", err)
	}
	t.Logf("retry draw id=%s posted=%v (first=%s)", again, posted2, first)
	if !posted2 && again == first {
		t.Logf("VULNERABLE: retry replayed the refunded draw; nothing was drawn")
	}
	// If the retry paid, the wallet is back to 0 cash / 3500 credit.
	if !posted2 || again == first {
		t.Fatalf("the retry replayed the refunded payment: %s posted=%v", again, posted2)
	}
	mustFunds(t, f, "acme", "acme", 0, 3500)
	if _, err := f.Refund(ctx, plan); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 1000, 5000)
}
