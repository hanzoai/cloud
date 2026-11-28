package limits

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// The platform's own orgs hold a comped Enterprise plan whose usage the policy names
// at nothing (0 requests, 0 a day, 0¢, overage on): every call is past the plan, paid
// from the payer's own money. With none, a call is refused plan_allowance_used. On
// account the gate admits it, prepaid, and what it costs is owed; it keeps admitting
// however much is owed, and the plan's own refusals are untouched for everyone else.
func TestAnOrgOnAccountIsAdmittedPastAPlanThatIncludesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fin := finance.New(finance.Local(t.TempDir(), testMaster))
		t.Cleanup(func() { _ = fin.Close() })
		prev := finance.Current()
		finance.Publish(fin)
		t.Cleanup(func() { finance.Publish(prev) })

		w := newWorld(t, "enterprise", 20, "external:comp")
		w.l.funds = walletFunds
		w.l.flag = policy(saved(nil, "enterprise", 0, 0, 0, 0))

		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodePrepaid {
			t.Fatalf("an Enterprise comp with nothing in its account: grant %+v hit %+v, want prepaid_required", g, hit)
		}

		finance.SetAccount(func(_ context.Context, org string) bool { return org == "acme" })
		t.Cleanup(func() { finance.SetAccount(nil) })
		for i, cents := range []int64{2_500_000, 7_500_000} { // $25,000 then $75,000 owed
			g, hit := w.priced(premium, ClassPremium)
			if hit != nil || g == nil || g.Pays != PaysPrepaid || g.Plan != "enterprise" {
				t.Fatalf("call %d on account: grant %+v hit %+v, want prepaid on the enterprise plan", i, g, hit)
			}
			if err := fin.RecordUsage(context.Background(), money.UsageInput{
				Org: "acme", Subject: "acme", Amount: money.FromCents(cents), Model: premium, Ref: fmt.Sprintf("u%d", i), Cash: g.Cash,
			}); err != nil {
				t.Fatal(err)
			}
		}
		f, err := fin.Funds(context.Background(), "acme", "acme", false)
		if err != nil {
			t.Fatal(err)
		}
		if f.Owed.Cents() != 10_000_000 || !f.Account {
			t.Fatalf("owed %d account %v, want $100,000 owed on account", f.Owed.Cents(), f.Account)
		}
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("on account owing $100,000: grant %+v hit %+v, want still admitted", g, hit)
		}
	})
}

// Past what an int64 holds a balance in nano-dollars reads the most it holds, never a
// wrapped negative that would refuse the payer.
func TestNanosSaturate(t *testing.T) {
	huge := new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(1_000_000_000))
	if got := NanosOfAtto(huge.Add(huge, big.NewInt(1))); got != math.MaxInt64 {
		t.Fatalf("NanosOfAtto past int64 = %d, want %d", got, int64(math.MaxInt64))
	}
}
