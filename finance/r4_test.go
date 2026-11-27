package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// A refund and a dispute of one payment never take back more than it brought in.
func TestARefundAndADisputeTakeBackAtMostThePayment(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true})
	for _, ref := range []string{"refund:rf_1", "dispute:dp_1"} {
		if _, err := f.Clawback(ctx, ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: ref, Amount: money.FromCents(5000)}); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	org, _ := f.Cash(ctx, "acme", false)
	raw, _ := f.storeFor("acme", false)
	credit, _ := raw.Balance(ctx, walletAcct("acme"))
	if !org.Paid.IsZero() || credit.Sign() != 0 {
		t.Fatalf("after refund+dispute of one $50 payment: paid %s, credit account %s; want 0 and 0", org.Paid, credit)
	}
	// A dispute the merchant wins gives back what it took.
	if _, err := f.Restore(ctx, ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: "dispute:dp_1"}); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 0) // the dispute took nothing: the refund already had it all
}

// A dispute won restores exactly what its clawback took, once.
func TestADisputeWonRestoresItsClawback(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true})
	in := ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: "dispute:dp_1", Amount: money.FromCents(5000)}
	if _, err := f.Clawback(ctx, in); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 0)
	first, err := f.Restore(ctx, in)
	if err != nil || first == "" {
		t.Fatalf("restore = %q, %v", first, err)
	}
	if again, err := f.Restore(ctx, in); err != nil || again != first {
		t.Fatalf("restore again = %q, %v; want %s", again, err, first)
	}
	mustFunds(t, f, "acme", "acme", 5000, 0)
}

// Money a processor took back accrues nothing.
func TestAccrualNetsOutAClawback(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true})
	usage(t, f, "acme", "acme", 4000, "m1")
	if _, err := f.Clawback(ctx, ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: "dispute:dp_1", Amount: money.FromCents(5000)}); err != nil {
		t.Fatal(err)
	}
	if paid, err := f.SumCashUsageSince(ctx, "acme", false, 0); err != nil || paid != 0 {
		t.Fatalf("cash-paid usage after the payment was clawed back = %d, %v; want 0", paid, err)
	}
}

// A held payment refunded and retried pays from its hold again, once.
func TestAHeldPaymentRetriedAfterARefundUsesItsHold(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true})
	in := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2000), Ref: "job:1"}
	if _, _, err := f.Hold(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Draw(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refund(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, posted, err := f.Draw(ctx, in); err != nil || !posted {
		t.Fatalf("retry = posted %v, %v", posted, err)
	}
	raw, _ := f.storeFor("acme", false)
	held, _ := raw.Balance(ctx, heldAcct(cashAcct("acme"), "job:1"))
	if !held.IsZero() {
		t.Fatalf("the hold still keeps %s after the retry paid from it", held)
	}
	mustFunds(t, f, "acme", "acme", 3000, 0)
}
