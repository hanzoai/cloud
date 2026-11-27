package finance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/money"
)

func mustFunds(t *testing.T, f *ledgerFinance, org, subject string, cash, credit int64) {
	t.Helper()
	got, err := f.Funds(context.Background(), org, subject, false)
	if err != nil {
		t.Fatalf("funds(%s,%s): %v", org, subject, err)
	}
	if got.Cash.Cents() != cash || got.Credit.Cents() != credit {
		t.Fatalf("funds(%s,%s) = cash %d credit %d; want cash %d credit %d",
			org, subject, got.Cash.Cents(), got.Credit.Cents(), cash, credit)
	}
	mustBalance(t, f, org, subject, cash+credit)
}

func deposit(t *testing.T, f *ledgerFinance, in money.DepositInput) string {
	t.Helper()
	id, err := f.Deposit(context.Background(), in)
	if err != nil {
		t.Fatalf("deposit %+v: %v", in, err)
	}
	return id
}

func usage(t *testing.T, f *ledgerFinance, org, subject string, cents int64, ref string) {
	t.Helper()
	if err := f.RecordUsage(context.Background(), money.UsageInput{Org: org, Subject: subject, Amount: money.FromCents(cents), Ref: ref}); err != nil {
		t.Fatalf("usage %s: %v", ref, err)
	}
}

// Money a payment brought in is cash; money minted is credit; the balance is both.
func TestAPaymentIsCashAndAGrantIsCredit(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "grant-1", Notes: "Admin grant by z@hanzo.ai: welcome"})
	mustFunds(t, f, "acme", "acme", 0, 500)
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2000), Ref: "sq_pay_1", Cash: true})
	mustFunds(t, f, "acme", "acme", 2000, 500)
	// A person's wallet in a shared org is its own, in both buckets.
	deposit(t, f, money.DepositInput{Org: "hanzo", Subject: "hanzo/bob", Amount: money.FromCents(700), Ref: "sq_pay_2", Cash: true})
	mustFunds(t, f, "hanzo", "hanzo/bob", 700, 0)
	mustFunds(t, f, "hanzo", "hanzo", 0, 0)
}

// Cash is a payment's, so it names the payment; one payment credits once whichever
// path records it, and a ref already credited as the other source is another payment.
func TestCashNamesItsPaymentAndCreditsOnce(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Cash: true}); err == nil {
		t.Fatal("a cash deposit with no payment reference was accepted")
	}
	first := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "sq_1", Cash: true})
	if again := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "sq_1", Cash: true}); again != first {
		t.Fatalf("the same payment again = %s, want the first entry %s", again, first)
	}
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "sq_1"}); !errors.Is(err, ErrRefTaken) {
		t.Fatalf("the payment's ref minted as credit: %v, want ErrRefTaken", err)
	}
	mustFunds(t, f, "acme", "acme", 100, 0)
}

// Every debit takes cash first; credit covers the rest, and a wallet with no
// credit line never goes below zero (no customer incurs debt).
func TestADebitDrawsCashFirst(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "g"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "p", Cash: true})
	usage(t, f, "acme", "acme", 300, "m1")
	mustFunds(t, f, "acme", "acme", 700, 500)
	usage(t, f, "acme", "acme", 900, "m2") // 700 cash, then 200 credit
	mustFunds(t, f, "acme", "acme", 0, 300)
	usage(t, f, "acme", "acme", 400, "m3") // drains remaining 300 credit; never creates debt
	mustFunds(t, f, "acme", "acme", 0, 0)
	// No debt was incurred, so the next card top-up lands in full.
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(250), Ref: "p2", Cash: true})
	mustFunds(t, f, "acme", "acme", 250, 0)
	// The replay of a debit moves nothing.
	usage(t, f, "acme", "acme", 400, "m3")
	mustFunds(t, f, "acme", "acme", 250, 0)
}

// A cash-only debit — a model the usage policy refused granted credit for — never
// draws credit and never pushes cash below zero when the org has no credit line.
func TestACashOnlyDebitNeverDrawsCredit(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "g"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "p", Cash: true})
	cash := func(cents int64, ref string) {
		t.Helper()
		if err := f.RecordUsage(context.Background(), money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(cents), Ref: ref, Cash: true}); err != nil {
			t.Fatalf("cash-only usage %s: %v", ref, err)
		}
	}
	cash(300, "jev-1")
	mustFunds(t, f, "acme", "acme", 700, 500)
	cash(900, "jev-2") // 700 cash drawn, 0 owed, grant untouched
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "p2", Cash: true})
	mustFunds(t, f, "acme", "acme", 300, 500)
}

// A plan paid from the balance is refused unless the whole balance covers it, and
// takes cash first.
func TestAPaymentDrawsCashFirstAndNeedsTheWholeBalance(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "g"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(200), Ref: "p", Cash: true})
	if _, _, err := f.Draw(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(501), Ref: "plan:big"}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("a payment larger than the balance: %v, want ErrShortWallet", err)
	}
	if _, _, err := f.Draw(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(250), Ref: "plan:1"}); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 250)
}

// A hold sets aside cash first; the payment made from it spends the held cash first
// and gives the rest back to the bucket it came from; a return gives each part back.
func TestAHoldKeepsEachBucketApart(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(600), Ref: "g"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(400), Ref: "p", Cash: true})
	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(700), Ref: "x402_1"}
	if _, _, err := f.Hold(ctx, job); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 300) // 400 cash + 300 credit held
	pay := job
	pay.Amount = money.FromCents(500) // spends 400 held cash, 100 held credit, returns 200 credit
	if _, _, err := f.Draw(ctx, pay); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 500)

	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "p2", Cash: true})
	job2 := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "x402_2"}
	if _, _, err := f.Hold(ctx, job2); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 300)
	back, err := f.Return(ctx, job2)
	if err != nil || back.Cents() != 300 {
		t.Fatalf("return = %s, %v; want 300", back, err)
	}
	mustFunds(t, f, "acme", "acme", 100, 500)
}

// Minted credit is reversed out of the credit it became, which must still hold it; a
// payment is reversed out of cash first, then credit, and the wallet must cover it.
func TestAReversalTakesBackItsOwnBucket(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	pay := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(400), Ref: "p", Cash: true})
	grant := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "g"})
	usage(t, f, "acme", "acme", 100, "m1") // cash 300
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: pay}); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 900) // 300 cash, then 100 credit
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: grant}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("reversing a grant partly spent: %v, want ErrShortWallet", err)
	}
	big := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "p2", Cash: true})
	usage(t, f, "acme", "acme", 4500, "m2") // cash 500, credit 900
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: big}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("reversing a payment the wallet cannot cover: %v, want ErrShortWallet", err)
	}
	mustFunds(t, f, "acme", "acme", 500, 900)
}

// fundsOf reports what a wallet can spend: never negative, never more cash than the
// balance.
func TestFundsOf(t *testing.T) {
	for _, c := range []struct{ cash, credit, wantCash, wantCredit int64 }{
		{0, 0, 0, 0},
		{500, 200, 500, 200},
		{500, -200, 300, 0},
		{500, -700, 0, 0},
		{0, -5, 0, 0},
		{0, 900, 0, 900},
	} {
		got := fundsOf(money.FromCents(c.cash), money.FromCents(c.credit))
		if got.Cash.Cents() != c.wantCash || got.Credit.Cents() != c.wantCredit {
			t.Errorf("fundsOf(%d, %d) = (%d, %d); want (%d, %d)", c.cash, c.credit,
				got.Cash.Cents(), got.Credit.Cents(), c.wantCash, c.wantCredit)
		}
	}
}

// Source replays a wallet cash first and posts one entry that moves what is left of
// its payments into cash; the balance does not move, it posts once, and a dry run
// posts nothing.
func TestSourceMovesTheUnspentPartOfEarlierPayments(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	// Before the ledger kept cash apart, a payment and a grant both landed as credit.
	paid := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(1000), Ref: "sq_legacy"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500), Ref: "grant-x"})
	usage(t, f, "acme", "acme/bob", 300, "m1") // cash first: 700 of the payment left
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(9000), Ref: "grant-pool"})
	mustFunds(t, f, "acme", "acme/bob", 0, 1200)

	dry, err := f.Source(ctx, "acme", false, map[string]bool{paid: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry) != 1 || dry[0].Wallet != "wallet:bob" || dry[0].Paid.Cents() != 1000 || dry[0].Cash.Cents() != 700 || dry[0].Posted != "" {
		t.Fatalf("dry run = %+v; want wallet:bob paid 1000, cash 700, nothing posted", dry)
	}
	mustFunds(t, f, "acme", "acme/bob", 0, 1200)

	done, err := f.Source(ctx, "acme", false, map[string]bool{paid: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Posted == "" {
		t.Fatalf("apply = %+v; want a posted entry", done)
	}
	mustFunds(t, f, "acme", "acme/bob", 700, 500)
	mustFunds(t, f, "acme", "acme", 0, 9000)

	again, err := f.Source(ctx, "acme", false, map[string]bool{paid: true}, true)
	if err != nil || len(again) != 1 || !again[0].Done || again[0].Posted != done[0].Posted {
		t.Fatalf("apply again = %+v, %v; want the first entry and nothing new", again, err)
	}
	mustFunds(t, f, "acme", "acme/bob", 700, 500)

	org, err := f.Cash(ctx, "acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if !org.Books || org.Paid.Cents() != 1000 || org.Owed.Cents() != 700 || org.Spent.Cents() != 300 || org.Credit.Cents() != 9500 {
		t.Fatalf("org cash = paid %d owed %d spent %d credit %d; want 1000/700/300/9500",
			org.Paid.Cents(), org.Owed.Cents(), org.Spent.Cents(), org.Credit.Cents())
	}

	// The wallet's movements still add up to its balance, with the source entry among them.
	rows, err := f.ListEntries(ctx, "acme", "acme/bob", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sourced bool
	for _, r := range rows {
		sourced = sourced || r.Kind == KindSource
	}
	if !sourced {
		t.Fatalf("the source entry is not among bob's movements: %+v", rows)
	}
}

// A payment named for Source that is not a deposit in these books refuses the whole
// run, and a reversed payment carries nothing.
func TestSourceRefusesWhatItCannotFind(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	pay := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(200), Ref: "sq_x"})
	if _, err := f.Source(ctx, "acme", false, map[string]bool{"dep_nope": true}, true); err == nil {
		t.Fatal("an unknown entry was accepted")
	}
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: pay}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Source(ctx, "acme", false, map[string]bool{pay: true}, true)
	if err != nil || len(got) != 1 || got[0].Paid.Sign() != 0 || got[0].Posted != "" {
		t.Fatalf("a reversed payment = %+v, %v; want nothing paid and nothing posted", got, err)
	}
	mustFunds(t, f, "acme", "acme", 0, 0)
}

// Cash across every org opens nothing: an org with no books answers so, and no file
// appears for it.
func TestCashOpensNoBooks(t *testing.T) {
	dir := t.TempDir()
	f := New(Local(dir, testMaster))
	defer func() { _ = f.Close() }()
	got, err := f.Cash(context.Background(), "nobody", false)
	if err != nil || got.Books {
		t.Fatalf("cash(nobody) = %+v, %v; want no books", got, err)
	}
	if rows, err := f.Deposits(context.Background(), "nobody", false); err != nil || len(rows) != 0 {
		t.Fatalf("deposits(nobody) = %+v, %v; want none", rows, err)
	}
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("reading an org with no books wrote %v", files)
	}
}

// A payment sourced after it was written is reversed out of cash first, and the books
// stop counting it as paid; minted credit beside it stays where it is.
func TestAReversedSourcedPaymentTakesItsCash(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	wire := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "grant-wire"})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "grant-comp"})
	if _, err := f.Source(ctx, "acme", false, map[string]bool{wire: true}, true); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 10_000, 10_000)
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: wire, Reason: "wire bounced"}); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 10_000)
	org, err := f.Cash(ctx, "acme", false)
	if err != nil || org.Paid.Sign() != 0 || org.Owed.Sign() != 0 {
		t.Fatalf("after the reversal paid %s owed %s, %v; want nothing", org.Paid, org.Owed, err)
	}
	// The settlement a sourced deposit came from, arriving again as cash, is the same
	// payment and credits nothing new.
	sq := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(700), Ref: "sq_old"})
	if _, err := f.Source(ctx, "acme", false, map[string]bool{sq: true}, true); err != nil {
		t.Fatal(err)
	}
	if again := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(700), Ref: "sq_old", Cash: true}); again != sq {
		t.Fatalf("the sourced payment again as cash = %s, want %s", again, sq)
	}
	mustFunds(t, f, "acme", "acme/bob", 700, 0)
}

// A payment reversal never takes credit while the wallet's cash is held: the hold
// would come back as cash and minted credit would have paid for the reversal.
func TestAReversalWaitsForHeldCash(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	pay := deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "sq_1", Cash: true})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "promo"})
	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "x402_job"}
	if _, _, err := f.Hold(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: pay}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("reversing a payment whose cash is held: %v, want ErrShortWallet", err)
	}
	if _, err := f.Return(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: pay}); err != nil {
		t.Fatal(err)
	}
	mustFunds(t, f, "acme", "acme", 0, 10_000)
}

// What cash paid for is the part of usage the cash accounts carried; credit-funded
// usage is not in it.
func TestSumCashUsageSinceCountsCashOnly(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "sq", Cash: true})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "promo"})
	usage(t, f, "acme", "acme", 500, "m1") // 300 cash, 200 credit
	usage(t, f, "acme", "acme", 100, "m2") // credit
	if got, err := f.SumCashUsageSince(ctx, "acme", false, 0); err != nil || got != 300 {
		t.Fatalf("cash-paid usage = %d, %v; want 300", got, err)
	}
	if got, err := f.SumUsageSince(ctx, "acme", false, "", "", 0); err != nil || got != 600 {
		t.Fatalf("all usage = %d, %v; want 600", got, err)
	}
}

// A refund or dispute takes a payment's money back out of cash first, then credit,
// once per refund; money these books never credited as cash is refused.
func TestAClawbackTakesAPaymentBack(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "promo"})
	usage(t, f, "acme", "acme", 4000, "m1") // cash 1000 left
	in := ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: "refund:rf_1", Amount: money.FromCents(3000), Reason: "refund.updated rf_1"}
	id, err := f.Clawback(ctx, in)
	if err != nil || id == "" {
		t.Fatalf("clawback = %q, %v", id, err)
	}
	mustFunds(t, f, "acme", "acme", 0, 0) // 1000 cash, then 2000 of credit: 1000 owed
	if again, err := f.Clawback(ctx, in); err != nil || again != id {
		t.Fatalf("the same refund again = %q, %v; want %s", again, err, id)
	}
	org, _ := f.Cash(ctx, "acme", false)
	if org.Paid.Cents() != 2000 {
		t.Fatalf("paid after the clawback = %d, want 2000", org.Paid.Cents())
	}
	if _, err := f.Clawback(ctx, ClawbackInput{Org: "acme", Subject: "acme", Payment: "promo", Ref: "refund:rf_2", Amount: money.FromCents(100)}); err == nil {
		t.Fatal("a clawback of minted credit was accepted")
	}
}
