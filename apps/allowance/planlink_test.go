package allowance

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/account"
)

// When an organization borrows a paid plan through a Shared Plan link, bounded plan
// quotas (such as daily/hourly automation runs) are counted atomically against the
// plan holder's single counter — shared across every member of the borrowing org and
// the lender, and never reset by unlinking and relinking to another org.
func TestSharedPlanSharesOneAtomicAllowanceAcrossOrgAndLender(t *testing.T) {
	var currentLinkedOrg atomic.Value
	currentLinkedOrg.Store("carbon")
	lender := account.Person(account.SignupOrg, "james")

	s := &service{
		store: testStore(t),
		tier: func(_ context.Context, subject, org string) (string, error) {
			if subject == lender.Subject() && org == lender.Org() {
				return "starter", nil // starter: 20 runs/day, 6 runs/hour
			}
			if cur, _ := currentLinkedOrg.Load().(string); cur != "" && org == cur && subject == cur {
				return "starter", nil
			}
			return "free", nil
		},
		linked: func(_ context.Context, org string) account.Account {
			if cur, _ := currentLinkedOrg.Load().(string); cur != "" && org == cur {
				return lender
			}
			return account.Account{}
		},
	}

	ctx := context.Background()
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	// Borrowing org "carbon" takes 4 automation runs in this hour.
	for i := range 4 {
		out, err := s.take(ctx, runs, "carbon", "carbon", "carbon", at.Add(time.Duration(i)*time.Second))
		if err != nil || out.Spent {
			t.Fatalf("carbon run %d refused early: %+v (%v)", i+1, out, err)
		}
	}

	// Lender "hanzo/james" takes 2 automation runs in the same hour -> reaches the 6/hour ceiling!
	for i := range 2 {
		out, err := s.take(ctx, runs, "hanzo", lender.Subject(), lender.Subject(), at.Add(time.Duration(10+i)*time.Second))
		if err != nil || out.Spent {
			t.Fatalf("lender run %d refused early: %+v (%v)", i+1, out, err)
		}
	}

	// 7th run in the hour is refused for BOTH "carbon" AND "hanzo/james".
	if out, err := s.take(ctx, runs, "carbon", "carbon", "carbon", at.Add(20*time.Second)); err != nil || !out.Spent {
		t.Fatalf("carbon 7th run should be refused on shared hourly quota, got %+v (%v)", out, err)
	}
	if out, err := s.take(ctx, runs, "hanzo", lender.Subject(), lender.Subject(), at.Add(21*time.Second)); err != nil || !out.Spent {
		t.Fatalf("lender 7th run should be refused on shared hourly quota, got %+v (%v)", out, err)
	}

	// Unlinking "carbon" and linking "other-org" STILL hits the same lender quota (no reset!).
	currentLinkedOrg.Store("other-org")
	if out, err := s.take(ctx, runs, "other-org", "other-org", "other-org", at.Add(22*time.Second)); err != nil || !out.Spent {
		t.Fatalf("relinking to other-org must not reset plan quota, got %+v (%v)", out, err)
	}
}
