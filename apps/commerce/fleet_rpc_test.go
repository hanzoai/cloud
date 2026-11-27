// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// Every org's subscriptions, in one read: an org past the readers' first batch is
// there with its plan, an org commerce holds with nothing bought is there with
// none, and each row is what the org's own read answers it.
func TestTheFleetReadListsEveryOrgsSubscriptions(t *testing.T) {
	bootRecord(t)
	var paying []string
	for i := range fleetReaders + 3 {
		org := fmt.Sprintf("fleet-%02d", i)
		if _, err := planeSubscriptionRecord(caller(org, true), paidFor(t, org)); err != nil {
			t.Fatalf("record %s: %v", org, err)
		}
		paying = append(paying, org)
	}
	if _, err := commerceorg.Resolve(context.Background(), "fleet-none"); err != nil {
		t.Fatal(err)
	}

	out, err := planeFleetSubscriptions(caller(authz.AdminOrg, true), nil)
	if err != nil {
		t.Fatalf("fleet read: %v", err)
	}
	got := map[string]client.OrgSubscriptions{}
	for _, o := range out.Orgs {
		got[o.Org] = o
	}
	for _, org := range paying {
		o, ok := got[org]
		if !ok || o.Error != "" || len(o.Rows) != 1 {
			t.Fatalf("%s: %+v (listed %v), want its one plan", org, o, ok)
		}
		own, err := planeSubscriptions(caller(org, false), &client.SubsIn{})
		if err != nil || own.Count != 1 || own.Rows[0].ID != o.Rows[0].ID || own.Rows[0].MRRCents != o.Rows[0].MRRCents {
			t.Fatalf("%s: the fleet read %+v, the org's own %+v (%v)", org, o.Rows, own, err)
		}
	}
	if o, ok := got["fleet-none"]; !ok || o.Error != "" || len(o.Rows) != 0 {
		t.Fatalf("fleet-none: %+v (listed %v), want listed with no plan", o, ok)
	}
}

// It reads every tenant's books, so only the platform's SuperAdmin may: a SuperAdmin
// acting in a customer org is refused, and so is the platform org without one.
func TestTheFleetReadIsThePlatformSuperAdmins(t *testing.T) {
	bootRecord(t)
	for _, who := range []context.Context{caller("acme", true), caller(authz.AdminOrg, false), caller("", true)} {
		_, err := planeFleetSubscriptions(who, nil)
		var ze *zip.HTTPError
		if !errors.As(err, &ze) || ze.Status != 403 {
			t.Fatalf("caller %+v: %v, want refused", zip.CallerOf(who), err)
		}
	}
}

// A fleet read that runs out its own time answers anyway, every org it did not reach
// listed unread with the deadline's error: the caller's deadline does not cross the
// plane, and an answer sent after the board stopped waiting is no answer at all.
func TestAFleetReadOutOfTimeAnswersWithEveryOrgUnread(t *testing.T) {
	bootRecord(t)
	if _, err := planeSubscriptionRecord(caller("slow", true), paidFor(t, "slow")); err != nil {
		t.Fatal(err)
	}
	was := fleetWait
	fleetWait = time.Nanosecond
	t.Cleanup(func() { fleetWait = was })

	out, err := planeFleetSubscriptions(caller(authz.AdminOrg, true), nil)
	if err != nil {
		t.Fatalf("a read out of time was refused: %v", err)
	}
	var slow *client.OrgSubscriptions
	for i := range out.Orgs {
		if out.Orgs[i].Org == "slow" {
			slow = &out.Orgs[i]
		}
	}
	if slow == nil || slow.Error == "" || len(slow.Rows) != 0 {
		t.Fatalf("slow: %+v, want it listed unread with the deadline's error", slow)
	}
}

// An org the caller's deadline cut off is listed with that error, never as empty; and
// a caller whose deadline passed before any org was listed is answered an error, never
// an empty fleet.
func TestAnOrgPastTheDeadlineIsListedUnread(t *testing.T) {
	bootRecord(t)
	if _, err := planeSubscriptionRecord(caller("late", true), paidFor(t, "late")); err != nil {
		t.Fatal(err)
	}
	o, err := commerceorg.Resolve(context.Background(), "late")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(caller(authz.AdminOrg, true))
	cancel()
	if row := fleetOrg(ctx, o); row.Org != "late" || row.Error == "" || len(row.Rows) != 0 {
		t.Fatalf("late: %+v, want it listed unread with the deadline's error", row)
	}
	if out, err := planeFleetSubscriptions(ctx, nil); err == nil {
		t.Fatalf("a caller past its deadline was answered %d orgs, want an error", len(out.Orgs))
	}
}
