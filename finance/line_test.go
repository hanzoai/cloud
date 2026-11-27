package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
)

func setLimit(t *testing.T, f *ledgerFinance, org string, cents int64) {
	t.Helper()
	if _, err := f.SetLimit(context.Background(), org, false, money.FromCents(cents)); err != nil {
		t.Fatalf("set limit %s: %v", org, err)
	}
}

func mustLine(t *testing.T, f *ledgerFinance, org string, cash, credit, line, owed, limit int64) {
	t.Helper()
	got, err := f.Funds(context.Background(), org, org, false)
	if err != nil {
		t.Fatalf("funds %s: %v", org, err)
	}
	if got.Cash.Cents() != cash || got.Credit.Cents() != credit || got.Line.Cents() != line ||
		got.Owed.Cents() != owed || got.Limit.Cents() != limit {
		t.Fatalf("funds %s = cash %d credit %d line %d owed %d limit %d; want %d %d %d %d %d", org,
			got.Cash.Cents(), got.Credit.Cents(), got.Line.Cents(), got.Owed.Cents(), got.Limit.Cents(),
			cash, credit, line, owed, limit)
	}
	if want := cash + line + credit; got.Total().Cents() != want {
		t.Fatalf("funds %s total = %d; want cash+line+credit %d", org, got.Total().Cents(), want)
	}
}

// An org with no cash and a $10 line spends to -$10: every call debits, the debt is
// owed, and once the line is drawn nothing is left to admit the next priced call.
func TestALineSpendsBelowZeroToItsLimit(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	mustLine(t, f, "acme", 0, 0, 0, 0, 0)
	setLimit(t, f, "acme", 1000)
	mustLine(t, f, "acme", 0, 0, 1000, 0, 1000)

	usage(t, f, "acme", "acme", 400, "u1")
	mustLine(t, f, "acme", 0, 0, 600, 400, 1000)
	usage(t, f, "acme", "acme", 600, "u2")
	mustLine(t, f, "acme", 0, 0, 0, 1000, 1000)
	mustBalance(t, f, "acme", "acme", 0)
}

// A payment pays down what is owed first: $15 into a wallet $10 in debt leaves $5 of
// cash and nothing due, and the line is whole again.
func TestADepositPaysTheDebtFirst(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	setLimit(t, f, "acme", 1000)
	usage(t, f, "acme", "acme", 1000, "u1")
	mustLine(t, f, "acme", 0, 0, 0, 1000, 1000)
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1500), Ref: "sq_1", Cash: true})
	mustLine(t, f, "acme", 500, 0, 1000, 0, 1000)
}

// The owner's order: cash, then the line, then granted credit.
func TestTheDrawOrderIsCashThenLineThenCredit(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Ref: "sq_1", Cash: true})
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "grant-1"})
	setLimit(t, f, "acme", 1000)

	usage(t, f, "acme", "acme", 200, "u1") // cash
	mustLine(t, f, "acme", 100, 500, 1000, 0, 1000)
	usage(t, f, "acme", "acme", 600, "u2") // the last 100 of cash, then 500 of line
	mustLine(t, f, "acme", 0, 500, 500, 500, 1000)
	usage(t, f, "acme", "acme", 700, "u3") // the last 500 of line, then 200 of credit
	mustLine(t, f, "acme", 0, 300, 0, 1000, 1000)

	rows := uses(t, f, "acme")
	want := map[string]string{"u1": "prepaid", "u2": "prepaid+line", "u3": "line+credits"}
	for ref, by := range want {
		if rows[ref] != by {
			t.Errorf("use %s paid by %q; want %q", ref, rows[ref], by)
		}
	}
}

// A cash-only usage is paid by cash and the line, never by granted credit, and
// stops at the credit limit — never creating debt beyond the line.
func TestACashOnlyUsageNeverDrawsCredit(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "grant-1"})
	setLimit(t, f, "acme", 300)
	ctx := context.Background()
	if err := f.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "jev-1", Cash: true}); err != nil {
		t.Fatal(err)
	}
	mustLine(t, f, "acme", 0, 5000, 0, 300, 300)
	if got := uses(t, f, "acme")["jev-1"]; got != "line+hanzo" {
		t.Fatalf("cash-only use paid by %q; want line+hanzo", got)
	}
}

// No org without an explicit credit line (or on-account switch) can ever incur
// debt: a usage debit on an empty or partial wallet drains only what the wallet
// holds, leaves Owed at 0, and never eats the customer's next card top-up.
func TestNoOrgWithoutCreditLineCanIncurDebt(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()

	// Empty wallet: usage is recorded as hanzo-absorbed with zero debt.
	usage(t, f, "acme", "acme", 250, "u-empty")
	mustLine(t, f, "acme", 0, 0, 0, 0, 0)
	if l, err := f.Line(ctx, "acme", false); err != nil || l.Since != 0 {
		t.Fatalf("empty wallet debt age = %d, %v; want 0", l.Since, err)
	}
	if got := uses(t, f, "acme")["u-empty"]; got != money.PaidByHanzo {
		t.Fatalf("empty wallet use paid by %q; want hanzo", got)
	}

	// Partial wallet: $0.50 credit + $1.00 usage drains $0.50, leaves $0 owed.
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(50), Ref: "g1"})
	usage(t, f, "acme", "acme", 100, "u-partial")
	mustLine(t, f, "acme", 0, 0, 0, 0, 0)
	if got := uses(t, f, "acme")["u-partial"]; got != "credits+hanzo" {
		t.Fatalf("partial use paid by %q; want credits+hanzo", got)
	}

	// Next card top-up lands 100% intact.
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "stripe_1", Cash: true})
	mustLine(t, f, "acme", 1000, 0, 0, 0, 0)
}

// The line is the org's own account's. A person's wallet in the same org never draws
// on it: in the signup org that would hand the platform's line to every stranger.
func TestALinePaysOnlyTheOrgsOwnAccount(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	setLimit(t, f, "hanzo", 100_000)
	got, err := f.Funds(context.Background(), "hanzo", "hanzo/alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Line.Sign() != 0 || got.Limit.Sign() != 0 || got.Total().Sign() != 0 {
		t.Fatalf("a member's wallet reads line %s limit %s total %s; want none", got.Line, got.Limit, got.Total())
	}
}

// A call the platform absorbs is counted: a $0 row with its cost, paid by hanzo, and
// no money moved.
func TestAFreeCallIsCountedAndPaidByHanzo(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()
	cost := money.FromCents(3)
	if err := f.RecordUsage(ctx, money.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.Zero(), Cost: cost,
		Model: "zen-free", Class: "free", Units: 1200, Ref: "free-1",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.ListUsage(ctx, "acme", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v; want the one free call", rows)
	}
	r := rows[0]
	if r.Amount.Sign() != 0 || r.Cost.Cmp(cost) != 0 || r.PaidBy != money.PaidByHanzo ||
		r.Class != "free" || r.Units != 1200 || r.Model != "zen-free" {
		t.Fatalf("free row = %+v; want $0, cost 3c, paid by hanzo, free, 1200 units", r)
	}
	mustLine(t, f, "acme", 0, 0, 0, 0, 0)
	// The replay counts it once.
	if err := f.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.Zero(), Cost: cost, Ref: "free-1"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.ListUsage(ctx, "acme", false, 10); len(rows) != 1 {
		t.Fatalf("replay counted twice: %+v", rows)
	}
}

// A call the plan covers is counted at its list price and moves no money.
func TestAPlanCallMovesNoMoney(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "sq_1", Cash: true})
	ctx := context.Background()
	if err := f.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(80), PaidBy: money.PaidByPlan, Ref: "p1"}); err != nil {
		t.Fatal(err)
	}
	mustLine(t, f, "acme", 500, 0, 0, 0, 0)
	if got := uses(t, f, "acme")["p1"]; got != money.PaidByPlan {
		t.Fatalf("plan use paid by %q", got)
	}
	if spent, _ := f.SumUsageSince(ctx, "acme", false, "", "", 0); spent != 0 {
		t.Fatalf("a plan call counted %d cents of wallet spend", spent)
	}
}

// A debit written before the usage log existed is listed from the journal, once, and
// a replay of its act neither debits nor counts it again.
func TestALegacyDebitIsListedOnce(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()
	store, err := f.storeFor("acme", false)
	if err != nil {
		t.Fatal(err)
	}
	amt := money.FromCents(100)
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		return tx.Insert(ledger.JournalEntry{
			ID: "use_legacy", Kind: string(KindUsage), Program: usageProgram("acme"), Ref: "u1",
			Memo: "zen-1", Amount: amt, CreatedAt: 1,
		}, []ledger.Posting{{Account: walletAcct("acme"), Amount: amt.Neg()}, {Account: acctRevenue, Amount: amt}})
	}); err != nil {
		t.Fatal(err)
	}
	usage(t, f, "acme", "acme", 100, "u1") // the same act again
	rows, err := f.ListUsage(ctx, "acme", false, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "use_legacy" || rows[0].Model != "zen-1" {
		t.Fatalf("rows = %+v, %v; want the one legacy debit", rows, err)
	}
	mustBalance(t, f, "acme", "acme", 0)
}

// Waive forgives an org's owed balance back to zero without minting cash or touching
// granted credit, reverses the phantom spend, and is idempotent on repeat calls.
func TestWaiveClearsOwedWithoutTouchingCreditOrMintingCash(t *testing.T) {
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	ctx := context.Background()

	// Seed an account matching Vishal's exact state: $25.13 owed on cash, $15.00 granted credit, $0 limit.
	setLimit(t, f, "acme", 2513)
	usage(t, f, "acme", "acme", 2513, "u1")
	setLimit(t, f, "acme", 0)
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1500), Ref: "grant-1"})
	mustLine(t, f, "acme", 0, 1500, 0, 2513, 0)

	got, err := f.Waive(ctx, "acme", false, "waive phantom local CLI runtime charges")
	if err != nil {
		t.Fatalf("waive: %v", err)
	}
	if got.Owed.Sign() != 0 || got.Cash.Sign() != 0 || got.Credit.Cents() != 1500 || got.Total().Cents() != 1500 {
		t.Fatalf("after waive: cash=%s credit=%s owed=%s total=%s; want 0 15.00 0 15.00",
			got.Cash, got.Credit, got.Owed, got.Total())
	}
	mustLine(t, f, "acme", 0, 1500, 0, 0, 0)

	// Month-to-date usage reverses back to 0 as well.
	if spent, err := f.SumUsageSince(ctx, "acme", false, "", "", 0); err != nil || spent != 0 {
		t.Fatalf("SumUsageSince after waive = %d, %v; want 0", spent, err)
	}

	// Second waive is a no-op; subsequent cash deposit stays 100% spendable cash.
	again, err := f.Waive(ctx, "acme", false, "repeat")
	if err != nil || again.Owed.Sign() != 0 || again.Credit.Cents() != 1500 {
		t.Fatalf("repeat waive = %+v, %v", again, err)
	}
	deposit(t, f, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2000), Ref: "sq_1", Cash: true})
	mustLine(t, f, "acme", 2000, 1500, 0, 0, 0)
}

// uses maps each counted call's ref to who paid it.
func uses(t *testing.T, f *ledgerFinance, org string) map[string]string {
	t.Helper()
	store, err := f.storeFor(org, false)
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.Uses(context.Background(), string(KindUsage), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, u := range all {
		out[u.Ref] = u.PaidBy
	}
	return out
}
