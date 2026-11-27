// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// planlink_ops_test.go — who may link and unlink a plan, and what is refused, through the
// plane ops the /v1/billing/link routes ask. The ops trust the Held list and the
// Privileged flag they are handed (decided from the caller's credential at the route,
// apps/billing/link_test.go); everything else is decided here, against real books.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// refusedWith fails unless err is a refusal with status and code.
func refusedWith(t *testing.T, err error, status int, code string) {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != status || he.Code != code {
		t.Fatalf("err = %v, want %d %q", err, status, code)
	}
}

func heldBy(accts ...account.Account) []string {
	out := make([]string, 0, len(accts))
	for _, a := range accts {
		out = append(out, a.Subject())
	}
	return out
}

// putLink links from to target as a caller holding held, privileged or not.
func putLink(target string, from account.Account, held []string, privileged bool) (*client.PlanLinkState, error) {
	return planeLinkPut(caller(target, false), &client.PlanLinkPutIn{
		From: from.Subject(), Held: held, By: "hanzo/ada", Privileged: privileged})
}

func TestAnAdminLinksAPlanTheyHold(t *testing.T) {
	bootRecord(t)
	src, tgt := account.Org("lp-src"), "lp-tgt"
	planOf(t, "lp-src", "lp-src")

	st, err := putLink(tgt, src, heldBy(src), true)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if st.Link == nil || !st.Link.Live || st.Link.Org != tgt || st.Link.From != "lp-src" ||
		st.Link.Plan != "go" || st.Link.PlanName == "" || st.Link.By != "hanzo/ada" || st.Link.At == "" {
		t.Fatalf("link = %+v, want a live go link from lp-src to %s by hanzo/ada", st.Link, tgt)
	}
	if got := tierOf(t, tgt, tgt); got.Plan != "go" {
		t.Fatalf("the linked org reads %+v, want go", got)
	}
	again, err := planeLink(caller(tgt, false), &client.PlanLinkIn{Held: heldBy(src)})
	if err != nil || again.Link == nil || again.Link.From != "lp-src" {
		t.Fatalf("read = %+v, %v; want the link", again, err)
	}
	if len(again.Plans) != 1 || again.Plans[0].From != "lp-src" || again.Plans[0].Org != tgt || again.Plans[0].Plan != "go" {
		t.Fatalf("plans = %+v; want lp-src's go plan, lent to %s", again.Plans, tgt)
	}
}

// A person's wallet in the signup org is lent by name, and only that person's plan.
func TestAPersonsSignupWalletIsLent(t *testing.T) {
	bootRecord(t)
	ada := account.Person(account.SignupOrg, "lpada")
	planOf(t, account.SignupOrg, ada.Subject())
	if _, err := putLink("lp-person-tgt", ada, heldBy(ada), true); err != nil {
		t.Fatalf("link: %v", err)
	}
	// A person's account outside the signup org is not a wallet anyone lends.
	other := account.Person("lp-other", "ada")
	planOf(t, "lp-other", other.Subject())
	_, err := putLink("lp-person-tgt2", other, heldBy(other), true)
	refusedWith(t, err, 400, client.LinkNoPlan)
}

// A member is not an admin, an admin of another org is not one here, and a plan the
// caller does not hold cannot be named, whatever its spelling shares with one they do.
func TestOnlyAnAdminLinksOnlyWhatTheyHold(t *testing.T) {
	bootRecord(t)
	src := account.Org("lauth-src")
	planOf(t, "lauth-src", "lauth-src")
	mine := account.Person(account.SignupOrg, "james")
	planOf(t, account.SignupOrg, "hanzo/lauthsam")
	theirs := account.Person("lauth-acme", "james") // the same name in another org

	_, err := putLink("lauth-tgt", src, heldBy(src), false)
	refusedWith(t, err, 403, "")
	_, err = putLink("lauth-tgt", src, heldBy(mine), true)
	refusedWith(t, err, 400, client.LinkNoPlan)
	_, err = putLink("lauth-tgt", theirs, heldBy(mine), true)
	refusedWith(t, err, 400, client.LinkNoPlan)
	_, err = putLink("lauth-tgt", account.Person(account.SignupOrg, "lauthsam"), nil, true)
	refusedWith(t, err, 400, client.LinkNoPlan)

	if s, _ := links(); s != nil {
		if _, ok, _ := s.ByOrg(context.Background(), "lauth-tgt"); ok {
			t.Fatal("a refused link left a row")
		}
	}
}

func TestAHeldAccountWithNoLivePlanCannotBeLinked(t *testing.T) {
	bootRecord(t)
	src := account.Org("lnone-src")
	_, err := putLink("lnone-tgt", src, heldBy(src), true)
	refusedWith(t, err, 400, client.LinkNoPlan)
}

func TestSomeOrgsCannotBorrowAPlan(t *testing.T) {
	bootRecord(t)
	src := account.Org("lbad-src")
	planOf(t, "lbad-src", "lbad-src")
	for _, target := range []string{account.SignupOrg, "admin", "lbad-src"} {
		_, err := putLink(target, src, heldBy(src), true)
		refusedWith(t, err, 400, client.LinkOrgRefused)
	}
	// The refusal does not wait on who asks: a member of the signup org is told the same.
	_, err := putLink(account.SignupOrg, src, heldBy(src), false)
	refusedWith(t, err, 400, client.LinkOrgRefused)
}

func TestAnOrgWithItsOwnPlanCannotBorrowOne(t *testing.T) {
	bootRecord(t)
	src := account.Org("lown-src")
	planOf(t, "lown-src", "lown-src")
	planOf(t, "lown-tgt", "lown-tgt")
	_, err := putLink("lown-tgt", src, heldBy(src), true)
	refusedWith(t, err, 409, client.LinkOrgHasPlan)
}

func TestOnePlanOneOrgAndOneOrgOnePlan(t *testing.T) {
	bootRecord(t)
	a, b := account.Org("lone-a"), account.Org("lone-b")
	planOf(t, "lone-a", "lone-a")
	planOf(t, "lone-b", "lone-b")
	held := heldBy(a, b)
	if _, err := putLink("lone-t1", a, held, true); err != nil {
		t.Fatal(err)
	}
	_, err := putLink("lone-t2", a, held, true)
	refusedWith(t, err, 409, client.LinkPlanTaken)
	_, err = putLink("lone-t1", b, held, true)
	refusedWith(t, err, 409, client.LinkOrgTaken)
	if got := tierOf(t, "lone-t2", "lone-t2"); got.Plan != "" {
		t.Fatalf("a refused link lent a plan: %+v", got)
	}
}

// Links racing for one plan, and for one org, are decided by the unique keys: one wins.
func TestRacingLinksLandOne(t *testing.T) {
	bootRecord(t)
	a, b := account.Org("lrace-a"), account.Org("lrace-b")
	planOf(t, "lrace-a", "lrace-a")
	planOf(t, "lrace-b", "lrace-b")
	held := heldBy(a, b)

	race := func(calls ...func() error) (won int, refused []error) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := call()
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					won++
				} else {
					refused = append(refused, err)
				}
			}()
		}
		wg.Wait()
		return
	}
	one := func(target string, from account.Account) func() error {
		return func() error { _, err := putLink(target, from, held, true); return err }
	}
	won, refused := race(one("lrace-t1", a), one("lrace-t2", a), one("lrace-t3", a), one("lrace-t4", a))
	if won != 1 || len(refused) != 3 {
		t.Fatalf("%d links of one plan landed, %d refused; want 1 and 3", won, len(refused))
	}
	for _, err := range refused {
		refusedWith(t, err, 409, client.LinkPlanTaken)
	}
	won, refused = race(one("lrace-same", b), one("lrace-same", account.Org("lrace-a")), one("lrace-same", b))
	if won != 0 && won != 1 {
		t.Fatalf("%d links of one org landed", won)
	}
	if won+len(refused) != 3 {
		t.Fatalf("won %d refused %d", won, len(refused))
	}
}

func TestUnlinkByTheOrgsAdminOrByTheHolder(t *testing.T) {
	bootRecord(t)
	src := account.Org("lun-src")
	planOf(t, "lun-src", "lun-src")
	mine := heldBy(src)

	// Not linked: 404, for either door.
	_, err := planeLinkDrop(caller("lun-tgt", false), &client.PlanLinkDropIn{Held: mine, Privileged: true})
	refusedWith(t, err, 404, "")

	if _, err := putLink("lun-tgt", src, mine, true); err != nil {
		t.Fatal(err)
	}
	// A member who holds nothing of it cannot end it.
	_, err = planeLinkDrop(caller("lun-tgt", false), &client.PlanLinkDropIn{})
	refusedWith(t, err, 403, "")
	// The org's admin ends it, and the plan is free to lend again.
	st, err := planeLinkDrop(caller("lun-tgt", false), &client.PlanLinkDropIn{Privileged: true})
	if err != nil || st.Link != nil {
		t.Fatalf("admin unlink = %+v, %v; want no link", st, err)
	}
	if got := tierOf(t, "lun-tgt", "lun-tgt"); got.Plan != "" {
		t.Fatalf("an unlinked org still reads %+v", got)
	}

	// The holder takes it back from any org they act in — one that is not the borrower's.
	if _, err := putLink("lun-tgt", src, mine, true); err != nil {
		t.Fatalf("relink: %v", err)
	}
	_, err = planeLinkDrop(caller("lun-elsewhere", false), &client.PlanLinkDropIn{From: "lun-src"})
	refusedWith(t, err, 403, "") // holds nothing: the list is empty
	if _, err := planeLinkDrop(caller("lun-elsewhere", false), &client.PlanLinkDropIn{From: "lun-src", Held: mine}); err != nil {
		t.Fatalf("holder unlink from another org: %v", err)
	}
	if got := tierOf(t, "lun-tgt", "lun-tgt"); got.Plan != "" {
		t.Fatalf("a taken-back plan is still read: %+v", got)
	}
	// Unlinking what is not linked is 404.
	_, err = planeLinkDrop(caller("lun-elsewhere", false), &client.PlanLinkDropIn{From: "lun-src", Held: mine})
	refusedWith(t, err, 404, "")
}

// The holder of a plan, acting in the borrowing org without being its admin, can end
// the link of their own plan and no other.
func TestAHolderEndsOnlyTheirOwnLinkFromInsideTheOrg(t *testing.T) {
	bootRecord(t)
	a, b := account.Org("lin-a"), account.Org("lin-b")
	planOf(t, "lin-a", "lin-a")
	planOf(t, "lin-b", "lin-b")
	if _, err := putLink("lin-t1", a, heldBy(a), true); err != nil {
		t.Fatal(err)
	}
	if _, err := putLink("lin-t2", b, heldBy(b), true); err != nil {
		t.Fatal(err)
	}
	// b's holder, acting in t1 (a's borrower), is not an admin there and holds not a.
	_, err := planeLinkDrop(caller("lin-t1", false), &client.PlanLinkDropIn{Held: heldBy(b)})
	refusedWith(t, err, 403, "")
	// naming another org's link by its source is no better.
	_, err = planeLinkDrop(caller("lin-t1", false), &client.PlanLinkDropIn{From: "lin-a", Held: heldBy(b)})
	refusedWith(t, err, 403, "")
	if _, err := planeLinkDrop(caller("lin-t1", false), &client.PlanLinkDropIn{Held: heldBy(a)}); err != nil {
		t.Fatalf("the holder ends their own link from inside: %v", err)
	}
	if st, err := planeLink(caller("lin-t2", false), &client.PlanLinkIn{}); err != nil || st.Link == nil {
		t.Fatalf("b's link = %+v, %v; it was not the caller's to end", st, err)
	}
}

// One org's link is not another's to read, and a member's read lists only plans they hold.
func TestALinkIsReadOnlyByTheOrgItIsFor(t *testing.T) {
	bootRecord(t)
	src := account.Org("lread-src")
	planOf(t, "lread-src", "lread-src")
	if _, err := putLink("lread-tgt", src, heldBy(src), true); err != nil {
		t.Fatal(err)
	}
	st, err := planeLink(caller("lread-other", false), &client.PlanLinkIn{})
	if err != nil || st.Link != nil || st.Plans == nil || len(st.Plans) != 0 {
		t.Fatalf("another org reads %+v, %v; want no link and an empty array of plans", st, err)
	}
	member, err := planeLink(caller("lread-tgt", false), &client.PlanLinkIn{})
	if err != nil || member.Link == nil || len(member.Plans) != 0 {
		t.Fatalf("a member of the borrower reads %+v, %v; want the link and none of the plans", member, err)
	}
}

// A link is a read of someone else's plan and nothing more: the lent row is not the
// borrower's to cancel, and linking opens no platform authority.
func TestALinkGrantsNoRowAndNoAuthority(t *testing.T) {
	bootRecord(t)
	src := account.Org("lauthz-src")
	id := planOf(t, "lauthz-src", "lauthz-src")
	if _, err := putLink("lauthz-tgt", src, heldBy(src), true); err != nil {
		t.Fatal(err)
	}
	_, err := planeSubscriptionCancel(caller("lauthz-tgt", false), &client.SubscriptionRef{ID: id, Privileged: true})
	refusedWith(t, err, 404, "")
	_, err = planeSubscriptionRecord(caller("lauthz-tgt", false), paidFor(t, "lauthz-tgt"))
	refusedWith(t, err, 403, "")
	if subs, err := planeSubscriptions(caller("lauthz-tgt", false), &client.SubsIn{}); err != nil || subs.Count != 0 {
		t.Fatalf("the borrower lists %+v, %v; the lent row is not its own", subs, err)
	}
}

// The answer says what the caller may do from here, by the rules the writes enforce.
func TestTheStateSaysWhatTheCallerMayDo(t *testing.T) {
	bootRecord(t)
	src := account.Org("lcan-src")
	planOf(t, "lcan-src", "lcan-src")
	planOf(t, "lcan-has", "lcan-has")
	read := func(org string, held []string, privileged bool) *client.PlanLinkState {
		t.Helper()
		st, err := planeLink(caller(org, false), &client.PlanLinkIn{Held: held, Privileged: privileged})
		if err != nil {
			t.Fatalf("read %s: %v", org, err)
		}
		return st
	}
	may := func(st *client.PlanLinkState, link, unlink bool, who string) {
		t.Helper()
		if st.CanLink != link || st.CanUnlink != unlink {
			t.Fatalf("%s: canLink %v canUnlink %v, want %v %v", who, st.CanLink, st.CanUnlink, link, unlink)
		}
	}

	may(read("lcan-tgt", nil, false), false, false, "a member")
	may(read("lcan-tgt", heldBy(src), true), true, false, "an admin of an unlinked org on no plan")
	may(read("lcan-has", heldBy(src), true), false, false, "an admin of an org with its own plan")
	may(read(account.SignupOrg, heldBy(src), true), false, false, "an admin of the signup org")
	may(read("admin", heldBy(src), true), false, false, "an admin of the reserved org")

	if _, err := putLink("lcan-tgt", src, heldBy(src), true); err != nil {
		t.Fatal(err)
	}
	may(read("lcan-tgt", nil, false), false, false, "a member of the linked org")
	may(read("lcan-tgt", nil, true), false, true, "an admin of the linked org")
	may(read("lcan-tgt", heldBy(src), false), false, true, "the holder of the lent plan, acting in the linked org")
	may(read("lcan-other", heldBy(src), true), true, false, "an admin of another org the holder could lend to")
}
