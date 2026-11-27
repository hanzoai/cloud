// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// customer.go — A PAYER IS A CUSTOMER, WHOEVER SETTLED THE CHARGE.
//
// A card paid with the payer present carries their address, and emit files them as a
// customer of the org they paid (apps/crm Customer). A renewal and an auto-reload are
// charged by a sweep with nobody present, and their rows name only the billing account
// that paid, "<org>/<name>" for a member. The address is IAM's: the sweep reads the
// directory once, as the platform, and files each payer the way a card payment does.
// A wire carries the address of the buyer its invoice was issued to (emit).
//
// The same read, taken over the subscriptions already held, files the subscribers who
// have paid since before any of this was written: a SuperAdmin runs the backfill once.
// Filing is idempotent, so a payer filed again changes nothing, and a backfill that did
// not reach every org is run again.

import (
	"cmp"
	"context"
	"github.com/hanzoai/cloud/sdk/principal"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

// directoryFn is IAM's directory, read as the platform, a variable so a test answers it.
var directoryFn = func(ctx context.Context) (*client.Directory, error) {
	return iampeer.IAMDirectory(cloud.For(ctx, authz.AdminOrg))
}

// addresses is the verified address of each person IAM holds, by the billing account
// their payments are made from ("<org>/<name>"). A service account, an application and
// a barred person have none, and neither has a person who did not prove theirs.
func addresses(ctx context.Context) (map[string]string, error) {
	dir, err := directoryFn(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(dir.Users))
	for _, u := range dir.Users {
		if u.Email == "" || !u.Verified || u.Forbidden || (u.Type != "" && u.Type != "normal-user") {
			continue
		}
		out[u.Owner+"/"+u.Name] = strings.ToLower(strings.TrimSpace(u.Email))
	}
	return out, nil
}

// customers files the people behind a sweep's paid charges as customers of the orgs
// they paid. A charge from an org's own plan names the org and no person, so it files
// nobody; the rest are read from the directory once for the whole sweep, within their
// own budget. Nothing here can reach the sweep.
func customers(lg luxlog.Logger, put func(context.Context, string, crm.Buyer, time.Time) error, paid []payment) {
	var people []payment
	for _, p := range paid {
		if strings.Contains(p.subject, "/") {
			people = append(people, p)
		}
	}
	if len(people) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
	defer cancel()
	by, err := addresses(ctx)
	if err != nil {
		lg.Debug("a sweep's payers were not filed as customers: the directory did not read", "err", err)
		return
	}
	for _, p := range people {
		if ctx.Err() != nil {
			return
		}
		p.email = by[p.subject]
		filed(lg, p.ledger, customer(ctx, put, p.ledger, p))
	}
}

// Backfilled is what filing the subscribers already held came to.
type Backfilled struct {
	// Orgs is how many orgs' subscriptions were read.
	Orgs int `json:"orgs"`
	// Unread is how many orgs ran out of time before theirs were; run it again.
	Unread int `json:"unread"`
	// Subscribers is how many people have paid for a plan in an org that sells for money.
	Subscribers int `json:"subscribers"`
	// Filed is how many of them are customers of the org they pay.
	Filed int `json:"filed"`
	// Skipped is how many were not filed: they have no verified address in IAM, or their
	// org holds no CRM.
	Skipped int `json:"skipped"`
}

// customerOps binds the backfill to the audit trail it is recorded on, as couponOps does.
type customerOps struct{ trail *audit.Recorder }

// exposeCustomer publishes the backfill. Use calls it.
func exposeCustomer(app *zip.App, trail *audit.Recorder) {
	o := customerOps{trail: trail}
	app.Post("/v1/commerce/customer/backfill", o.backfill,
		zip.WithOperationID("backfillCustomers"),
		zip.WithSummary("File every subscriber who has paid as a customer of the org they pay"),
		zip.WithTags("customer"))
}

// Files every person who has paid for a plan as a customer of the org they pay, in that
// org's CRM, for the subscribers held before payments filed their payers.
//
// The subscriptions are the fleet's (the board's read) and the addresses IAM's
// directory: a subscriber is a person with their own billing account, "<org>/<name>",
// who paid for a plan outside a sandbox. Each is filed as a payment files its payer:
// lifecycle and type customer unless the team gave another type, and the commerce user
// linked once. It is idempotent, so a repeat changes nothing. A subscriber with no
// verified address in IAM, or whose org holds no CRM, is skipped and counted; an org the
// read ran out of time before is counted unread, and the call is made again. No CRM is
// installed, and no money is read or moved.
//
// SuperAdmin of the platform only. The run is on the audit trail before it starts
// (admin.customers.backfill); a trail that does not take it refuses the run (503).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o customerOps) backfill(ctx context.Context, _ *cloud.Unit) (*Backfilled, error) {
	if !principal.SuperFrom(ctx) {
		return nil, zip.ErrForbidden("customers: the platform's SuperAdmin backfills them")
	}
	who := cloud.Who(ctx)
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the run, so it was not made")
	}
	if _, err := o.trail.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "admin.customers.backfill",
		Resource:  audit.Resource{Type: "customers", ID: "backfill"},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
	}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the run, so it was not made: %v", err)
	}
	fleet, err := planeFleetSubscriptions(ctx, nil)
	if err != nil {
		return nil, err
	}
	by, err := addresses(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "customers: the directory did not read: %v", err)
	}
	out, lg, put := &Backfilled{}, luxlog.Default(), file
	for _, org := range fleet.Orgs {
		if org.Error != "" {
			out.Unread++
			continue
		}
		out.Orgs++
		seen := map[string]bool{}
		for _, s := range org.Rows {
			if s.Test || (s.LastPaid == "" && s.ChargedCents == 0) || !strings.Contains(s.UserID, "/") || seen[s.UserID] {
				continue
			}
			seen[s.UserID] = true
			out.Subscribers++
			if ctx.Err() != nil {
				return out, nil
			}
			// A context with no request behind it, or the filing would act in the
			// SuperAdmin's own org, which is no customer's CRM.
			p := payment{ledger: org.Org, subject: s.UserID, email: by[s.UserID]}
			if p.email == "" {
				out.Skipped++
				continue
			}
			if err := customer(context.Background(), put, org.Org, p); err != nil {
				filed(lg, org.Org, err)
				out.Skipped++
				continue
			}
			out.Filed++
		}
	}
	return out, nil
}
