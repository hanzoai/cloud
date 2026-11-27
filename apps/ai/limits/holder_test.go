// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package limits

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/account"
)

// ownedWorld is a person, hanzo/ann, on the Unlimited plan, and an org, proj, whose
// books hold no plan and no money. owner says whether ann owns proj.
func ownedWorld(t *testing.T, owner bool) *Limiter {
	t.Helper()
	now := periodStart.Add(26*time.Hour + 30*time.Minute)
	row := client.Subscription{
		PlanID: "rec-unlimited", Status: "active", Quantity: 1, ID: "sub-ann", ProviderType: "square", Settled: "card",
		Seats: 1, ChargedCents: 99500,
		CurrentPeriodStart: periodStart.Format(time.RFC3339),
		CurrentPeriodEnd:   periodStart.AddDate(0, 1, 0).Format(time.RFC3339),
		Plan:               client.SubscriptionPlan{Interval: "month", Price: 99500},
	}
	ann := func(subject, org string) bool { return subject == "hanzo/ann" && org == "hanzo" }
	dir := t.TempDir()
	empty(t, dir)
	l, err := Open(dir,
		func(_ context.Context, subject, org string) (string, string, error) {
			if ann(subject, org) {
				return "unlimited", row.ID, nil
			}
			return "", "", nil
		},
		func(_ context.Context, subject, org string) ([]client.Subscription, error) {
			if ann(subject, org) {
				return []client.Subscription{row}, nil
			}
			return nil, nil
		},
		shipped,
		func(context.Context, string, string) (int64, int64, error) { return 0, 0, nil },
		policy(flags.CarriedValues()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.store.Close() })
	l.now = func() time.Time { return now }
	l.holderOf = func(_ context.Context, person, org string) account.Account {
		if owner && person == "hanzo/ann" && org == "proj" {
			return account.PayerOf("", person)
		}
		return account.Account{}
	}
	return l
}

// A key in an org its owner owns spends the owner's Unlimited plan: every model,
// with no session or day window, however many requests.
func TestAnOwnersUnlimitedPlanPaysForTheirOrgsKeys(t *testing.T) {
	l := ownedWorld(t, true)
	ctx := context.Background()
	for i := range 400 { // past every capped rung's session
		for _, q := range []Ask{
			{Model: premium, Class: ClassPremium, Priced: true, Spend: true},
			{Model: free, Class: ClassFree, Family: "enso", Spend: true},
		} {
			q.Subject, q.Org, q.Actor = "proj", "proj", "hanzo/ann"
			g, hit, err := l.Admit(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if hit != nil {
				t.Fatalf("request %d (%s) refused: %+v", i, q.Model, hit)
			}
			if g == nil || g.Pays != PaysPlan || g.Plan != "unlimited" {
				t.Fatalf("request %d (%s): want the owner's plan to pay, got %+v", i, q.Model, g)
			}
		}
	}
}

// A member of an org someone else owns lends it no plan: the org's books decide, and
// with nothing in them a priced model is refused and the free lane is left to the
// free allowance.
func TestAMembersPlanDoesNotPayForAnOrgTheyDoNotOwn(t *testing.T) {
	l := ownedWorld(t, false)
	ctx := context.Background()
	g, hit, err := l.Admit(ctx, Ask{Subject: "proj", Org: "proj", Actor: "hanzo/ann", Model: premium, Class: ClassPremium, Priced: true, Spend: true})
	if err != nil {
		t.Fatal(err)
	}
	if g != nil || hit == nil {
		t.Fatalf("want a refusal, got grant %+v hit %+v", g, hit)
	}
	g, hit, err = l.Admit(ctx, Ask{Subject: "proj", Org: "proj", Actor: "hanzo/ann", Model: free, Class: ClassFree, Family: "enso", Spend: true})
	if err != nil || g != nil || hit != nil {
		t.Fatalf("want the free lane left to the allowance, got %+v %+v %v", g, hit, err)
	}
}

// The person's own calls at home are their own plan's, as before.
func TestAPersonsOwnCallsSpendTheirPlan(t *testing.T) {
	l := ownedWorld(t, false)
	g, hit, err := l.Admit(context.Background(), Ask{Subject: "hanzo/ann", Org: "hanzo", Actor: "hanzo/ann", Model: premium, Class: ClassPremium, Priced: true, Spend: true})
	if err != nil || hit != nil || g == nil || g.Pays != PaysPlan {
		t.Fatalf("want the plan to pay, got %+v %+v %v", g, hit, err)
	}
}
