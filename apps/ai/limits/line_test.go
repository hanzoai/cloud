package limits

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// jev is a model the seed policy keeps off granted credit (grantsPay false) with a
// free cap of 0: only a plan or the payer's own money pays for it.
const jev = "typesafe/jev-1.13"

// ledger is a real finance ledger, published as this process's, and a limiter with no
// plan that reads it through walletFunds: the composition the ai gate runs.
type ledger struct {
	t   *testing.T
	fin interface {
		finance.Funder
		finance.Liner
		finance.Reverser
		Deposit(context.Context, money.DepositInput) (string, error)
		RecordUsage(context.Context, money.UsageInput) error
		ListUsage(context.Context, string, bool, int) ([]finance.UsageRow, error)
	}
	l *Limiter
}

func newLedger(t *testing.T) *ledger {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	t.Cleanup(func() { _ = fin.Close() })
	prev := finance.Current()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(prev) })
	l, err := Open(t.TempDir(),
		func(context.Context, string, string) (string, string, error) { return "", "", nil },
		func(context.Context, string, string) ([]client.Subscription, error) { return nil, nil },
		nil, walletFunds, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.store.Close() })
	at := periodStart.Add(26 * time.Hour) // one UTC day: a daily cap cannot roll mid-test
	l.now = func() time.Time { return at }
	return &ledger{t: t, fin: fin, l: l}
}

func (w *ledger) limit(cents int64) {
	w.t.Helper()
	if _, err := w.fin.SetLimit(context.Background(), "acme", false, money.FromCents(cents)); err != nil {
		w.t.Fatal(err)
	}
}

// ask decides one priced request from subject in acme.
func (w *ledger) ask(subject, model, class string) (*Grant, *Hit) {
	w.t.Helper()
	g, hit, err := w.l.Admit(context.Background(), Ask{Subject: subject, Org: "acme", Model: model, Class: class, Priced: true, Spend: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return g, hit
}

// use records what a grant's call cost, as the gate's debit does.
func (w *ledger) use(g *Grant, cents int64, ref string) {
	w.t.Helper()
	if err := w.fin.RecordUsage(context.Background(), money.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(cents), Model: jev, Ref: ref, Cash: g.Cash,
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *ledger) funds() finance.Funds {
	w.t.Helper()
	f, err := w.fin.Funds(context.Background(), "acme", "acme", false)
	if err != nil {
		w.t.Fatal(err)
	}
	return f
}

// An org with no cash and a credit line is admitted to Jev, which granted credit never
// pays: prepaid, its own money only, and the debit is drawn from the line. Once the
// line is drawn the next request is refused insufficient_balance, granted credit or not.
func TestALinePaysForJevUntilItIsDrawn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		if _, err := w.fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100000), Ref: "grant-1"}); err != nil {
			t.Fatal(err)
		}
		w.limit(5000)

		g, hit := w.ask("acme", jev, ClassOurs)
		if hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("jev on a line: grant %+v hit %+v, want prepaid, own money only", g, hit)
		}
		w.use(g, 1000, "u1")
		rows, err := w.fin.ListUsage(context.Background(), "acme", false, 10)
		if err != nil || len(rows) != 1 || rows[0].PaidBy != money.PaidByLine {
			t.Fatalf("usage rows %+v (%v), want one paid by the line", rows, err)
		}
		if f := w.funds(); f.Owed.Cents() != 1000 || f.Line.Cents() != 4000 || f.Credit.Cents() != 100000 {
			t.Fatalf("after one call: owed %d line %d credit %d, want 1000 4000 100000", f.Owed.Cents(), f.Line.Cents(), f.Credit.Cents())
		}

		w.use(g, 4000, "u2")
		if g, hit := w.ask("acme", jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev on a drawn line: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
	})
}

// With no line nothing changes: granted credit alone never opens Jev or any premium
// model, and a model that takes granted credit, Hanzo's own, is still paid from it.
func TestWithNoLineJevStillNeedsAPaidPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		if g, hit := w.ask("acme", jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev with nothing: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		if _, err := w.fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100000), Ref: "grant-1"}); err != nil {
			t.Fatal(err)
		}
		if g, hit := w.ask("acme", jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev on granted credit: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		if g, hit := w.ask("acme", premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("premium on granted credit: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		if g, hit := w.ask("acme", "zen5-pro", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
			t.Fatalf("a model granted credit pays for: grant %+v hit %+v, want credits", g, hit)
		}
	})
}

// Cash pays for a third party's model and granted credit never does, on the ledger
// itself: beside $1,000 of granted credit a premium model is refused; once $5 of cash
// lands it is admitted cash only, and its debit takes cash and leaves every granted
// cent where it was.
func TestCashPaysForAThirdPartysModelAndCreditNever(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		ctx := context.Background()
		if _, err := w.fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100000), Ref: "grant-1"}); err != nil {
			t.Fatal(err)
		}
		if g, hit := w.ask("acme", premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("premium on granted credit: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		if _, err := w.fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "wire:1", Cash: true}); err != nil {
			t.Fatal(err)
		}
		g, hit := w.ask("acme", premium, ClassPremium)
		if hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("premium with cash: grant %+v hit %+v, want prepaid, cash only", g, hit)
		}
		w.use(g, 120, "u1")
		if f := w.funds(); f.Cash.Cents() != 380 || f.Credit.Cents() != 100000 || f.Owed.Cents() != 0 {
			t.Fatalf("after one call: cash %d credit %d owed %d, want 380 100000 0", f.Cash.Cents(), f.Credit.Cents(), f.Owed.Cents())
		}
		rows, err := w.fin.ListUsage(ctx, "acme", false, 10)
		if err != nil || len(rows) != 1 || rows[0].PaidBy != money.PaidByPrepaid {
			t.Fatalf("usage rows %+v (%v), want one paid from cash", rows, err)
		}
	})
}

// The line is the org's own account's (finance lined): a member's own wallet, as in the
// signup org, draws none of it, so the line opens nothing for that member.
func TestAMembersWalletDrawsNoLine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		w.limit(5000)
		if g, hit := w.ask("acme/bob", jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev from a member wallet beside a line: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		if g, hit := w.ask("acme", jev, ClassOurs); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("jev from the org's own account: grant %+v hit %+v, want prepaid", g, hit)
		}
	})
}

// Kai past its free plan's daily cap is refused free_plan_cap; once the org has a line
// with room it is admitted, prepaid, and granted credit may pay after the line.
func TestALineAdmitsAModelPastItsFreeCap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		for i := 1; i <= 500; i++ {
			if g, hit := w.ask("acme", "kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysFree {
				t.Fatalf("kai request %d: grant %+v hit %+v, want free", i, g, hit)
			}
		}
		if g, hit := w.ask("acme", "kai", ClassOurs); g != nil || hit == nil || hit.Code != CodeFreePlanCap {
			t.Fatalf("kai past the cap: grant %+v hit %+v, want free_plan_cap", g, hit)
		}
		w.limit(5000)
		if g, hit := w.ask("acme", "kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysPrepaid || g.Cash {
			t.Fatalf("kai past the cap on a line: grant %+v hit %+v, want prepaid, credit allowed", g, hit)
		}
	})
}

// Cash a payment recorded outside the card processor deposits — a wire, keyed on its
// reference — is the customer's own money: it raises cash and not credit, it opens Jev,
// and reversing the deposit closes it again.
func TestAWireOpensJevAndItsReversalClosesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		entry, err := w.fin.Deposit(context.Background(), money.DepositInput{
			Org: "acme", Subject: "acme", Amount: money.FromCents(20000), Ref: "wire:BOA-7818", Cash: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if f := w.funds(); f.Cash.Cents() != 20000 || f.Credit.Sign() != 0 {
			t.Fatalf("after the wire: cash %d credit %d, want 20000 and 0", f.Cash.Cents(), f.Credit.Cents())
		}
		if g, hit := w.ask("acme", jev, ClassOurs); hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("jev on wired cash: grant %+v hit %+v, want prepaid, own money only", g, hit)
		}
		if _, err := w.fin.Reverse(context.Background(), finance.ReverseInput{Org: "acme", Entry: entry, Reason: "bounced"}); err != nil {
			t.Fatal(err)
		}
		if f := w.funds(); f.Cash.Sign() != 0 {
			t.Fatalf("after the reversal: cash %d, want 0", f.Cash.Cents())
		}
		if g, hit := w.ask("acme", jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev after the wire was reversed: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
	})
}

// Over the plane the line crosses in the balance reply and is the payer's own money
// beside its cash; a reply from a ledger that reports no line reads none.
func TestThePlaneReadCountsTheLine(t *testing.T) {
	usd := func(d string) client.Money { return client.Money{Decimal: d, Currency: "USD"} }
	f, err := balanceFunds(&client.Balance{Amount: usd("150"), Cash: usd("0"), Credit: usd("100"), Line: usd("50")})
	if err != nil {
		t.Fatal(err)
	}
	if f.Own().Cents() != 5000 || f.Credit.Cents() != 10000 {
		t.Fatalf("own %d credit %d, want 5000 and 10000", f.Own().Cents(), f.Credit.Cents())
	}
	f, err = balanceFunds(&client.Balance{Amount: usd("100"), Cash: usd("0"), Credit: usd("100")})
	if err != nil {
		t.Fatal(err)
	}
	if f.Own().Sign() != 0 {
		t.Fatalf("no line reported: own %d, want 0", f.Own().Cents())
	}
	if _, err := balanceFunds(&client.Balance{Cash: usd("x")}); err == nil {
		t.Fatal("an unreadable cash figure read as a number")
	}
}

// WHO WOULD PAY, per class, is the policy's own answer for a payer with no plan: nothing
// while the wallet holds nothing, granted credit for the classes that take it, and
// prepaid for every class once cash lands. A picker shows Top up on "" and never reads
// the credit rule itself.
func TestAPayerWithNoPlanReadsWhoWouldPayPerClass(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newLedger(t)
		ctx := context.Background()
		pays := func() map[string]string {
			t.Helper()
			s, err := w.l.Read(ctx, "acme", "acme")
			if err != nil {
				t.Fatal(err)
			}
			return s.Pays
		}
		if got := pays(); got[ClassPremium] != "" || got[ClassOurs] != "" || len(got) != 2 {
			t.Fatalf("an empty wallet: %v, want nothing paying either class", got)
		}
		if _, err := w.fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100000), Ref: "grant-1"}); err != nil {
			t.Fatal(err)
		}
		if got := pays(); got[ClassPremium] != "" || got[ClassOurs] != PaysCredits {
			t.Fatalf("granted credit: %v, want credits for ours and nothing for premium", got)
		}
		if _, err := w.fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "wire:1", Cash: true}); err != nil {
			t.Fatal(err)
		}
		if got := pays(); got[ClassPremium] != PaysPrepaid || got[ClassOurs] != PaysPrepaid {
			t.Fatalf("cash: %v, want prepaid for both", got)
		}
	})
}
