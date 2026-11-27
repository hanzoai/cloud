// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/audit"
)

// people stands IAM's directory in, and counts how often it is read.
func people(t *testing.T, users ...client.DirectoryUser) *int {
	t.Helper()
	reads := new(int)
	prior := directoryFn
	directoryFn = func(context.Context) (*client.Directory, error) {
		*reads++
		return &client.Directory{Users: users}, nil
	}
	t.Cleanup(func() { directoryFn = prior })
	return reads
}

// person is a verified account of org.
func person(org, name, email string) client.DirectoryUser {
	return client.DirectoryUser{Owner: org, Name: name, Email: email, Verified: true, Type: "normal-user"}
}

// A sweep's charge has no address, so its payers are filed from IAM's directory, read
// once for the whole sweep: a member with a verified address is a customer of the org
// they paid, and the org's own plan, an unverified address, a service account and a
// sandbox charge file nobody. A sweep with nobody to name reads no directory.
func TestSweeps_FileTheirPayersAsCustomers(t *testing.T) {
	mute(t)
	filings := watchCustomers(t)
	sweeps = riskGate(luxlog.New("test"))
	reads := people(t,
		person("acme", "alice", "Alice@Example.com"),
		client.DirectoryUser{Owner: "acme", Name: "bob", Email: "bob@example.com", Type: "normal-user"},
		client.DirectoryUser{Owner: "acme", Name: "svc", Email: "svc@example.com", Verified: true, Type: "service-account"},
		person("zeta", "zoe", "zoe@example.com"),
	)

	renewed([]commercebilling.Renewal{
		{OrgName: "acme", UserId: "acme/alice", InvoiceId: "inv_1", AmountCents: 2000, Currency: "usd", Success: true},
		{OrgName: "acme", UserId: "acme/bob", InvoiceId: "inv_2", AmountCents: 2000, Currency: "usd", Success: true},
		{OrgName: "acme", UserId: "acme/svc", InvoiceId: "inv_3", AmountCents: 2000, Currency: "usd", Success: true},
		{OrgName: "acme", UserId: "acme", InvoiceId: "inv_4", AmountCents: 2000, Currency: "usd", Success: true},
		{OrgName: "acme", UserId: "acme/alice", InvoiceId: "inv_5", AmountCents: 2000, Currency: "usd", Success: true, Test: true},
	})
	reloaded([]commercebilling.RechargeResult{
		{OrgName: "zeta", UserId: "zeta/zoe", Charged: true, AmountCents: 2500, Currency: "usd", TransactionId: "tx_9"},
	})

	var got []filing
	for len(got) < 2 {
		select {
		case f := <-filings:
			got = append(got, f)
		case <-time.After(3 * time.Second):
			t.Fatalf("filed %+v, want alice and zoe", got)
		}
	}
	select {
	case f := <-filings:
		t.Errorf("a payer with no verified address, a service account, the org's own plan or a sandbox charge was filed: %+v", f)
	case <-time.After(200 * time.Millisecond):
	}
	slices.SortFunc(got, func(a, b filing) int { return strings.Compare(a.org, b.org) })
	if got[0] != (filing{org: "acme", buyer: crm.Buyer{Email: "alice@example.com", User: "acme/alice"}}) ||
		got[1] != (filing{org: "zeta", buyer: crm.Buyer{Email: "zoe@example.com", User: "zeta/zoe"}}) {
		t.Errorf("filed %+v, want alice in acme and zoe in zeta, by their verified addresses", got)
	}
	if *reads != 2 {
		t.Errorf("the directory was read %d times, want once for each sweep", *reads)
	}

	renewed([]commercebilling.Renewal{{OrgName: "acme", UserId: "acme", InvoiceId: "inv_6", AmountCents: 2000, Currency: "usd", Success: true}})
	if *reads != 2 {
		t.Errorf("a sweep with no person to name read the directory")
	}
}

// trailed is an audit trail to record on.
func trailed(t *testing.T) *audit.Recorder {
	t.Helper()
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	return trail
}

// The backfill files each subscriber who has paid, by their verified address, as a
// customer of the org they pay, once an org however many plans they hold and again
// without change; it skips and counts a subscriber IAM has no verified address for,
// leaves out an org's own plan, and is on the audit trail.
func TestTheBackfillFilesEverySubscriberWhoHasPaid(t *testing.T) {
	bootRecord(t)
	for _, subject := range []string{"bf-a/ada", "bf-a/zed", "bf-b/ada", "bf-c"} {
		org, _, _ := strings.Cut(subject, "/")
		in := paidFor(t, subject)
		if _, err := planeSubscriptionRecord(caller(org, true), in); err != nil {
			t.Fatalf("record %s: %v", subject, err)
		}
	}
	filings := watchCustomers(t)
	people(t, person("bf-a", "ada", "ada@example.com"), person("bf-b", "ada", "ada@example.com"))
	trail := trailed(t)
	op := customerOps{trail: trail}
	admin := caller(authz.AdminOrg, true)

	for range 2 {
		out, err := op.backfill(admin, nil)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if out.Subscribers != 3 || out.Filed != 2 || out.Skipped != 1 || out.Unread != 0 {
			t.Fatalf("backfilled %+v, want 3 subscribers: 2 filed, zed skipped, bf-c's own plan left out", out)
		}
		var got []filing
		for len(got) < 2 {
			select {
			case f := <-filings:
				got = append(got, f)
			case <-time.After(3 * time.Second):
				t.Fatalf("filed %+v", got)
			}
		}
		slices.SortFunc(got, func(a, b filing) int { return strings.Compare(a.org, b.org) })
		want := []filing{
			{org: "bf-a", buyer: crm.Buyer{Email: "ada@example.com", User: "bf-a/ada"}},
			{org: "bf-b", buyer: crm.Buyer{Email: "ada@example.com", User: "bf-b/ada"}},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("filed %+v, want %+v", got, want)
		}
	}

	rows, _, err := trail.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	for _, r := range rows {
		if r.Action == "admin.customers.backfill" {
			runs++
		}
	}
	if runs != 2 {
		t.Fatalf("%d runs on the audit trail, want 2", runs)
	}
}

// Only the platform's SuperAdmin backfills, and nothing is read when the audit trail
// does not take the run.
func TestTheBackfillIsThePlatformSuperAdminsAndAudited(t *testing.T) {
	bootRecord(t)
	people(t)
	filings := watchCustomers(t)
	for _, who := range []context.Context{caller("acme", true), caller(authz.AdminOrg, false), caller("", true)} {
		_, err := customerOps{trail: trailed(t)}.backfill(who, nil)
		var ze *zip.HTTPError
		if !asHTTP(err, &ze) || ze.Status != http.StatusForbidden {
			t.Fatalf("caller %+v: %v, want refused", zip.CallerOf(who), err)
		}
	}
	_, err := customerOps{}.backfill(caller(authz.AdminOrg, true), nil)
	var ze *zip.HTTPError
	if !asHTTP(err, &ze) || ze.Status != http.StatusServiceUnavailable {
		t.Fatalf("no audit trail: %v, want 503", err)
	}
	select {
	case f := <-filings:
		t.Fatalf("a refused run filed %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}
