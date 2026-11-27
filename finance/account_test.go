package finance

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
)

// onLux puts "lux" and "hanzo" on account for one test.
func onLux(t *testing.T) {
	t.Helper()
	SetAccount(func(_ context.Context, org string) bool { return org == "lux" || org == "hanzo" })
	t.Cleanup(func() { SetAccount(nil) })
}

func mustOwn(t *testing.T, f *ledgerFinance, org string) Line {
	t.Helper()
	l, err := f.Line(context.Background(), org, false)
	if err != nil {
		t.Fatalf("line %s: %v", org, err)
	}
	return l
}

// An org on account with nothing in it may still spend: its line left is open
// whatever it owes, every call it makes is owed, and the gate's total never runs out.
func TestAnOrgOnAccountIsNeverOutOfFunds(t *testing.T) {
	onLux(t)
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()

	got, err := f.Funds(ctx, "lux", "lux", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Account || got.Line.Cmp(open) != 0 || got.Total().Cmp(open) != 0 {
		t.Fatalf("an org on account with no books: account %v line %s total %s; want true and %s", got.Account, got.Line, got.Total(), open)
	}
	usage(t, f, "lux", "lux", 4500, "u1")
	usage(t, f, "lux", "lux", 500, "u2")
	got, err = f.Funds(ctx, "lux", "lux", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owed.Cents() != 5000 || got.Cash.Sign() != 0 || got.Line.Cmp(open) != 0 {
		t.Fatalf("after $50 of usage: owed %d cash %s line %s; want 5000 owed, no cash, line %s", got.Owed.Cents(), got.Cash, got.Line, open)
	}
	rows := uses(t, f, "lux")
	if rows["u1"] != "line" || rows["u2"] != "line" {
		t.Fatalf("usage on account paid by %v; want line", rows)
	}
	l := mustOwn(t, f, "lux")
	if !l.Account || l.Owed.Cents() != 5000 || l.Used.Cents() != 5000 || l.Since == 0 || l.Period != monthOf(time.Now()) {
		t.Fatalf("line = account %v owed %d used %d since %d period %d", l.Account, l.Owed.Cents(), l.Used.Cents(), l.Since, l.Period)
	}

	// An org that is not on the switch keeps the line it was given, none here.
	got, err = f.Funds(ctx, "acme", "acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account || got.Line.Sign() != 0 || got.Total().Sign() != 0 {
		t.Fatalf("an org not on account: account %v line %s total %s; want nothing", got.Account, got.Line, got.Total())
	}
}

// Only the org's own account is on account: in the signup org, a person's own wallet
// is theirs, and the platform's arrears never pay for it.
func TestAMembersWalletIsNotOnAccount(t *testing.T) {
	onLux(t)
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	got, err := f.Funds(context.Background(), "hanzo", "hanzo/bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account || got.Line.Sign() != 0 || got.Total().Sign() != 0 {
		t.Fatalf("hanzo/bob: account %v line %s total %s; want a wallet of his own with nothing in it", got.Account, got.Line, got.Total())
	}
	mustBalance(t, f, "hanzo", "hanzo/bob", 0)
	if bal, err := f.Balance(context.Background(), "hanzo", "hanzo", "usd", false); err != nil || bal.Cmp(open) != 0 {
		t.Fatalf("the hanzo org's own account = %s, %v; want %s", bal, err, open)
	}
}

// A payment pays the debt down and leaves its age alone until it is all paid; the
// next call owed after that starts a new one.
func TestTheDebtsAgeIsWhenItBegan(t *testing.T) {
	onLux(t)
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()

	usage(t, f, "lux", "lux", 1000, "u1")
	if l := mustOwn(t, f, "lux"); l.Since == 0 {
		t.Fatal("the first call owed did not record when the debt began")
	}
	// Say the debt began long ago, then pay part of it: it is as old as it was.
	store, err := f.storeFor("lux", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Tx(ctx, func(tx ledger.Tx) error { return tx.(interface{ Owe(int64) error }).Owe(1) }); err != nil {
		t.Fatal(err)
	}
	deposit(t, f, money.DepositInput{Org: "lux", Subject: "lux", Amount: money.FromCents(400), Ref: "wire:A1", Cash: true})
	if l := mustOwn(t, f, "lux"); l.Owed.Cents() != 600 || l.Since != 1 {
		t.Fatalf("after a part payment: owed %d since %d; want 600 since 1", l.Owed.Cents(), l.Since)
	}
	usage(t, f, "lux", "lux", 100, "u2")
	if l := mustOwn(t, f, "lux"); l.Since != 1 {
		t.Fatalf("a call on a standing debt moved its age to %d", l.Since)
	}
	// Paid in full it is not owed, and has no age.
	deposit(t, f, money.DepositInput{Org: "lux", Subject: "lux", Amount: money.FromCents(700), Ref: "wire:A2", Cash: true})
	if l := mustOwn(t, f, "lux"); l.Owed.Sign() != 0 || l.Since != 0 {
		t.Fatalf("paid in full: owed %s since %d; want nothing", l.Owed, l.Since)
	}
	// The next debt begins now.
	start := time.Now().Unix()
	usage(t, f, "lux", "lux", 300, "u3")
	if l := mustOwn(t, f, "lux"); l.Owed.Cents() != 300 || l.Since < start {
		t.Fatalf("a new debt: owed %d since %d; want 300 since >= %d", l.Owed.Cents(), l.Since, start)
	}
}
