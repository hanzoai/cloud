// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// planlink_read_test.go — what a plan link changes in the two plan reads, run through the
// real ops on the real embed (bootRecord) with plans recorded the way a wire is.

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// planOf records a paid go plan held by subject in org, and answers its subscription id.
func planOf(t *testing.T, org, subject string) string {
	t.Helper()
	in := paidFor(t, org)
	in.Subject = subject
	in.Reference = client.PaymentRef{Invoice: "inv:" + subject, Payment: "pay_" + subject}
	got, err := planeSubscriptionRecord(caller(org, true), in)
	if err != nil {
		t.Fatalf("record a plan for %s in %s: %v", subject, org, err)
	}
	return got.Subscription.ID
}

// lend links target to from, as the store records it.
func lend(t *testing.T, target string, from account.Account) {
	t.Helper()
	s, err := links()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), linkOf(target, from)); err != nil {
		t.Fatalf("link %s to %s: %v", target, from.Subject(), err)
	}
}

// tierOf is what the tier op serves subject in org.
func tierOf(t *testing.T, org, subject string) *client.Tier {
	t.Helper()
	got, err := planeTier(caller(org, false), &client.TierIn{Subject: subject})
	if err != nil {
		t.Fatalf("tier of %s in %s: %v", subject, org, err)
	}
	return got
}

// paidPlan is what the paywall's read answers for org.
func paidPlan(t *testing.T, org string) (string, bool, error) {
	t.Helper()
	return InProcessClient().(*inProcessClient).ActivePaidPlan(context.Background(), org)
}

// An org on no plan of its own reads the plan of the account it borrows from, through
// both reads, with its own money beside it and no subscription it could cancel.
func TestALinkedOrgReadsTheSourcePlanThroughBothReads(t *testing.T) {
	bootRecord(t)
	const src, tgt = "link-src", "link-tgt"
	planOf(t, src, src)
	alone := tierOf(t, tgt, tgt)
	if alone.Plan != "" || alone.Tier.Name != "free" {
		t.Fatalf("an org with nothing reads %+v, want no plan", alone)
	}
	lend(t, tgt, account.Org(src))

	got := tierOf(t, tgt, tgt)
	if got.Plan != "go" || got.PlanName == "" || got.Tier.Name != "pro" {
		t.Fatalf("tier = %+v, want the source's go plan on the pro tier", got)
	}
	if got.User != tgt {
		t.Errorf("tier answers for %q, want the borrowing org %q", got.User, tgt)
	}
	if got.Subscription != "" {
		t.Errorf("tier names subscription %q; the row is not the borrowing org's to cancel", got.Subscription)
	}
	if got.Balance != alone.Balance {
		t.Errorf("balance = %+v, want the borrowing org's own %+v: no money crosses ledgers", got.Balance, alone.Balance)
	}
	if slug, paid, err := paidPlan(t, tgt); err != nil || !paid || slug != "go" {
		t.Fatalf("ActivePaidPlan = %q, %v, %v; want go, paid", slug, paid, err)
	}
	if slug, paid, err := paidPlan(t, "link-bystander"); err != nil || paid || slug != "" {
		t.Fatalf("an org linked to nothing reads %q, %v, %v; want no plan", slug, paid, err)
	}
}

// OwnPaidPlan is what an org PAYS for: a borrowed plan is served through ActivePaidPlan
// and never answered here, so nothing that records money against an org's plan (a
// promotion) can count the lender's plan for the borrower.
func TestOwnPaidPlanNeverAnswersABorrowedPlan(t *testing.T) {
	bootRecord(t)
	const src, tgt = "ownpaid-src", "ownpaid-tgt"
	planOf(t, src, src)
	lend(t, tgt, account.Org(src))
	c := InProcessClient().(*inProcessClient)

	if slug, paid, err := c.ActivePaidPlan(context.Background(), tgt); err != nil || !paid || slug != "go" {
		t.Fatalf("ActivePaidPlan = %q, %v, %v; the borrower is served the lent plan", slug, paid, err)
	}
	if slug, paid, err := c.OwnPaidPlan(context.Background(), tgt); err != nil || paid || slug != "" {
		t.Fatalf("OwnPaidPlan = %q, %v, %v; want no plan: the borrower pays for none", slug, paid, err)
	}
	if slug, paid, err := c.OwnPaidPlan(context.Background(), src); err != nil || !paid || slug != "go" {
		t.Fatalf("OwnPaidPlan for the lender = %q, %v, %v; want its own go plan", slug, paid, err)
	}
}

// An org's own live plan is never replaced by a link.
func TestAnOrgsOwnPlanWinsOverALink(t *testing.T) {
	bootRecord(t)
	const src, tgt = "own-src", "own-tgt"
	planOf(t, src, src)
	own := planOf(t, tgt, tgt)
	lend(t, tgt, account.Org(src))

	got := tierOf(t, tgt, tgt)
	if got.Plan != "go" || got.Subscription != own {
		t.Fatalf("tier = %+v, want the org's own row %s", got, own)
	}
	if slug, paid, err := paidPlan(t, tgt); err != nil || !paid || slug != "go" {
		t.Fatalf("ActivePaidPlan = %q, %v, %v; want go, paid", slug, paid, err)
	}
}

// A source whose plan has ended confers nothing, and the record stays until unlinked.
func TestALapsedSourceConfersNothing(t *testing.T) {
	bootRecord(t)
	const src, tgt = "lapse-src", "lapse-tgt"
	id := planOf(t, src, src)
	lend(t, tgt, account.Org(src))
	if got := tierOf(t, tgt, tgt); got.Plan != "go" {
		t.Fatalf("before it lapses: %+v, want go", got)
	}
	if _, err := planeSubscriptionCancel(caller(src, false), &client.SubscriptionRef{ID: id, Subject: src}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if got := tierOf(t, tgt, tgt); got.Plan != "" || got.Tier.Name != "free" {
		t.Fatalf("tier = %+v, want no plan once the source lapsed", got)
	}
	if slug, paid, err := paidPlan(t, tgt); err != nil || paid || slug != "" {
		t.Fatalf("ActivePaidPlan = %q, %v, %v; want no plan", slug, paid, err)
	}
	s, _ := links()
	if _, ok, _ := s.ByOrg(context.Background(), tgt); !ok {
		t.Fatal("a lapse removed the link; it stays until it is unlinked")
	}
}

// A person's wallet lends that person's plan and nobody else's in the same org.
func TestAPersonSourceLendsOnlyThatPersonsPlan(t *testing.T) {
	bootRecord(t)
	const home = "link-home"
	planOf(t, home, home+"/bob")
	lend(t, "link-for-alice", account.Person(home, "alice"))
	lend(t, "link-for-bob", account.Person(home, "bob"))

	if got := tierOf(t, "link-for-alice", "link-for-alice"); got.Plan != "" {
		t.Fatalf("alice has no plan but the borrower reads %+v: another person's plan was read", got)
	}
	if _, paid, err := paidPlan(t, "link-for-alice"); err != nil || paid {
		t.Fatalf("ActivePaidPlan for alice's borrower = %v, %v; want none", paid, err)
	}
	if got := tierOf(t, "link-for-bob", "link-for-bob"); got.Plan != "go" {
		t.Fatalf("bob's borrower reads %+v, want go", got)
	}
	if slug, paid, err := paidPlan(t, "link-for-bob"); err != nil || !paid || slug != "go" {
		t.Fatalf("ActivePaidPlan for bob's borrower = %q, %v, %v; want go", slug, paid, err)
	}
}

// A store this process cannot read is an error on both reads, never Free and never a
// grant.
func TestAnUnreadableLinkStoreIsAnErrorNotFree(t *testing.T) {
	bootRecord(t)
	old := planLinks.Swap(nil)
	t.Cleanup(func() { planLinks.Store(old) })

	_, err := planeTier(caller("unread-tgt", false), &client.TierIn{Subject: "unread-tgt"})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status < 500 {
		t.Fatalf("tier with no store: %v, want a 5xx", err)
	}
	if slug, paid, err := paidPlan(t, "unread-tgt"); err == nil || paid || slug != "" {
		t.Fatalf("ActivePaidPlan with no store = %q, %v, %v; want an error", slug, paid, err)
	}
}

// A link confers the plan's gates and nothing it pays out: the borrowing org's month
// shows no plan, no included allotment granted or promised, and no credits, while the
// source keeps its own.
func TestALinkMintsAndShowsNoIncludedAllotment(t *testing.T) {
	bootRecord(t)
	const src, tgt = "allot-src", "allot-tgt"
	planOf(t, src, src)
	lend(t, tgt, account.Org(src))

	if got := tierOf(t, tgt, tgt); got.Plan != "go" || got.Balance.CreditsRemaining != 0 {
		t.Fatalf("tier = %+v, want the go plan with no credits of the borrowing org's own", got)
	}
	r, err := planeRollup(caller(tgt, false), &client.RollupIn{Subject: tgt})
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if r.Plan != "" || r.Included.MonthlyCents != 0 || r.Included.GrantedCents != 0 || r.Balance.BalanceCents != 0 {
		t.Fatalf("the borrowing org's month = %+v; it must show no plan and no included allotment of the source's", r)
	}
	if again := tierOf(t, tgt, tgt); again.Balance.CreditsRemaining != 0 {
		t.Fatalf("reading the plan minted credits: %+v", again.Balance)
	}
}
