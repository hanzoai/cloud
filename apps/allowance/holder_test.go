package allowance

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/account"
)

// heldService is the allowance where hanzo/ann is on a paid plan (pro) and the org
// proj's books hold none; owner says whether ann owns proj.
func heldService(t *testing.T, owner bool) *service {
	t.Helper()
	return &service{
		store: testStore(t),
		tier: func(_ context.Context, subject, org string) (string, error) {
			if subject == "hanzo/ann" && org == "hanzo" {
				return "pro", nil
			}
			return "free", nil
		},
		holder: func(_ context.Context, person, org string) account.Account {
			if owner && person == "hanzo/ann" && org == "proj" {
				return account.PayerOf("", person)
			}
			return account.Account{}
		},
	}
}

// A key in an org its owner owns is held to the owner's plan: a paid plan sets no
// session or day, so no count of five hours ever refuses it.
func TestAnOwnersPaidPlanLiftsTheFreeWindowOnTheirOrg(t *testing.T) {
	s := heldService(t, true)
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := range 3 * free {
		out, err := s.take(context.Background(), calls, "proj", "proj", "hanzo/ann", at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if out.Spent || out.Limit != 0 {
			t.Fatalf("call %d: want unbounded, got %+v", i+1, out)
		}
	}
}

// A member of an org someone else owns is held to that org's Free ceiling.
func TestAMembersPlanDoesNotLiftAnotherOrgsWindow(t *testing.T) {
	s := heldService(t, false)
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	var out = struct{ spent bool }{}
	for i := range free + 1 {
		a, err := s.take(context.Background(), calls, "proj", "proj", "hanzo/ann", at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		out.spent = a.Spent
	}
	if !out.spent {
		t.Fatalf("want call %d refused on the Free session", free+1)
	}
}
