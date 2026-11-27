package finance

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// A draw is all or nothing: a wallet that cannot cover it is refused with
// nothing moved, one that can is debited exactly once per ref, and the debit is
// the same revenue-recognized entry a metered call writes.
func TestDrawIsAllOrNothingAndOncePerRef(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()

	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(3000), Ref: "grant-1"}); err != nil {
		t.Fatal(err)
	}
	period := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Model: "subscription", Ref: "record:acme:retainer:1"}

	id, posted, err := f.Draw(ctx, period)
	if err != nil || !posted || id == "" {
		t.Fatalf("draw = (%q, %v, %v), want a posted entry", id, posted, err)
	}
	mustBalance(t, f, "acme", "acme", 500)

	again, posted, err := f.Draw(ctx, period)
	if err != nil || posted || again != id {
		t.Fatalf("replay = (%q, %v, %v), want the first entry %s and nothing moved", again, posted, err, id)
	}
	mustBalance(t, f, "acme", "acme", 500)

	next := period
	next.Ref = "invoice:inv_2:attempt:0"
	if _, _, err := f.Draw(ctx, next); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("draw of 2500 from 500: %v, want ErrShortWallet", err)
	}
	mustBalance(t, f, "acme", "acme", 500)

	reused := period
	reused.Amount = money.FromCents(100)
	if _, _, err := f.Draw(ctx, reused); !errors.Is(err, ErrRefReused) {
		t.Fatalf("a paid ref for another amount: %v, want ErrRefReused", err)
	}

	// A plan's payment is not AI spend: the cap and the usage page never see it,
	// and the org's ledger names it for what it is.
	spent, err := f.SumUsageSince(ctx, "acme", false, "", "", 0)
	if err != nil || spent != 0 {
		t.Fatalf("spend = %d, %v; a plan's payment counted as usage", spent, err)
	}
	if rows, err := f.ListUsage(ctx, "acme", false, 0); err != nil || len(rows) != 0 {
		t.Fatalf("usage page = %+v, %v; a plan's payment listed as usage", rows, err)
	}
	rows, err := f.ListEntries(ctx, "acme", "acme", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.ID == id {
			found = r.Kind == KindPayment && r.Amount.Cents() == 2500
		}
	}
	if !found {
		t.Fatalf("entries = %+v; want the draw %s as a payment of 2500", rows, id)
	}
}

// A draw's ref and a metered call's ref do not collide: each is unique within its
// own kind in the wallet.
func TestDrawAndUsageKeepTheirOwnRefs(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "g"}); err != nil {
		t.Fatal(err)
	}
	in := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "same"}
	if _, posted, err := f.Draw(ctx, in); err != nil || !posted {
		t.Fatalf("draw: %v, %v", posted, err)
	}
	if _, posted, err := f.RecordUsageOnce(ctx, in); err != nil || !posted {
		t.Fatalf("usage under a ref a payment holds: %v, %v", posted, err)
	}
	mustBalance(t, f, "acme", "acme", 400)
}

// A metered debit still lands whatever the wallet holds: its call was admitted
// before it ran, so refusing the debit afterwards would give the work away.
func TestMeteredDebitIsNotADraw(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()

	if _, _, err := f.RecordUsageOnce(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(700), Ref: "msg_1"}); err != nil {
		t.Fatalf("metered debit on an empty wallet: %v", err)
	}
	if _, _, err := f.Draw(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1), Ref: "record:acme:x:1"}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("draw from an overdrawn wallet: %v, want ErrShortWallet", err)
	}
}

// A payment for something not delivered is given back exactly: each bucket gets back
// what it gave, once, and a ref that drew nothing refunds nothing.
func TestARefundGivesEachBucketBack(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "sq", Cash: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "promo"}); err != nil {
		t.Fatal(err)
	}
	plan := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Ref: "record:acme:team:1"}
	if _, _, err := f.Draw(ctx, plan); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 3500)
	id, err := f.Refund(ctx, plan)
	if err != nil || id == "" {
		t.Fatalf("refund = %q, %v", id, err)
	}
	mustFunds(t, f, "acme", "acme", 1000, 5000)
	if again, err := f.Refund(ctx, plan); err != nil || again != id {
		t.Fatalf("the refund again = %q, %v; want the first %s", again, err, id)
	}
	mustFunds(t, f, "acme", "acme", 1000, 5000)
	if none, err := f.Refund(ctx, money.UsageInput{Org: "acme", Subject: "acme", Ref: "record:never"}); err != nil || none != "" {
		t.Fatalf("refunding a ref that drew nothing = %q, %v", none, err)
	}
}
