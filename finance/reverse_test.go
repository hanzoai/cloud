package finance

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// A reversal puts the wallet back where it was before the deposit, as a negative
// deposit beside the original, exactly once, and never as spend.
func TestReverseUndoesADepositOnce(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()

	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500)}); err != nil {
		t.Fatal(err)
	}
	grant, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(20000), Ref: "grant-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustBalance(t, f, "acme", "acme", 20500)

	r, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: grant, Reason: "paid for by a plan"})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if r.Of != grant || r.ID == "" || r.ID == grant || r.Amount.Cents() != 20000 || r.Wallet != "wallet" {
		t.Fatalf("reversal = %+v", r)
	}
	mustBalance(t, f, "acme", "acme", 500)

	again, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: grant})
	if !errors.Is(err, ErrReversed) || again.ID != r.ID {
		t.Fatalf("second reversal = (%+v, %v), want ErrReversed naming %s", again, err, r.ID)
	}
	mustBalance(t, f, "acme", "acme", 500)

	spent, err := f.SumUsageSince(ctx, "acme", false, "", "", 0)
	if err != nil || spent != 0 {
		t.Fatalf("a reversal read as spend: %d, %v", spent, err)
	}

	rows, err := f.ListEntries(ctx, "acme", "acme", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.ID == r.ID {
			found = true
			if row.Kind != KindDeposit || row.Amount.Cents() != -20000 || row.Ref != "reverse:"+grant {
				t.Errorf("reversal entry = %+v", row)
			}
		}
	}
	if !found {
		t.Error("the reversal is not in the wallet's entries")
	}
}

func TestReverseRefusesWhatItCannotUndo(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()

	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: "dep_missing"}); !errors.Is(err, ErrNoEntry) {
		t.Errorf("unknown entry: %v, want ErrNoEntry", err)
	}

	dep, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000)})
	if err != nil {
		t.Fatal(err)
	}
	use, _, err := f.RecordUsageOnce(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: use}); !errors.Is(err, ErrNotDeposit) {
		t.Errorf("usage entry: %v, want ErrNotDeposit", err)
	}
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: dep}); !errors.Is(err, ErrShortWallet) {
		t.Errorf("partly spent deposit: %v, want ErrShortWallet", err)
	}
	mustBalance(t, f, "acme", "acme", 700)
}
