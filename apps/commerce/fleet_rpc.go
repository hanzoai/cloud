// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// fleet_rpc.go — every org's subscriptions in one read, for the operator's board.
//
// The subscription board used to ask BillingSubscriptions once per org over the
// plane, 32 orgs at a time, beside five money reads per org, all inside one
// two-minute fold. With 1,354 orgs in the directory and about six seconds a call,
// the fold reached the first 222 orgs in directory order. Nearly all of them were
// one-per-wallet orgs with nothing bought, and the orgs that do subscribe were never
// asked, so the board read as empty.
//
// This answers the board in one call, reading the orgs the way commerce's own
// renewal sweep does (billing.RenewDue): every org in commerce's root store, then
// each org's rows from that org's own store. It skips the per-org plane round trip
// and the org resolution each call made, which creates an org commerce has not seen.

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// fleetReaders bounds how many orgs' stores the fleet read opens at once.
const fleetReaders = 16

// fleetWait is how long the fleet read reads before it answers with what it has. A
// caller's deadline does not cross the plane, and the board waits core.MemoFleetRead
// (75s) for this answer: with 1,560 orgs the read ran past it, so the board gave up and
// read every org's whole ledger one at a time instead, while this went on reading for
// nobody. Stopping here, every org not reached is listed unread (fleetOrg), and the
// board shows what was read and names the rest. A variable so a test can shorten it.
var fleetWait = 60 * time.Second

// Lists every org's subscriptions, in one read, for the platform's SuperAdmin.
//
// Each org's rows are what BillingSubscriptions answers that org. An org commerce
// does not hold is absent: commerce holds an org from the first call that names it,
// so one it has never seen has bought nothing. An org that does not read is listed
// with its error rather than left out or listed empty, and so is every org the
// caller's deadline cut off, so a reader can always tell an org with no plan from
// an org nobody read.
//
// It reads every tenant's books, so it answers the platform org's SuperAdmin and
// refuses everyone else, whatever the call names.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeFleetSubscriptions(ctx context.Context, _ *cloud.Unit) (*client.FleetSubscriptions, error) {
	if who := cloud.Who(ctx); who.Org != authz.AdminOrg || !who.Admin {
		return nil, zip.ErrForbidden("fleet subscriptions: the platform's SuperAdmin reads them")
	}
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"fleet subscriptions: commerce is not co-resident in this process")
	}
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(datastore.New(ctx)).GetAll(&orgs); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "fleet subscriptions: list organizations: %v", err)
	}
	// The orgs are read within fleetWait; the list above is not, so a read that runs
	// out of time still answers every org, the ones it did not reach as unread.
	read, cancel := context.WithTimeout(ctx, fleetWait)
	defer cancel()
	out := make([]client.OrgSubscriptions, len(orgs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(fleetReaders, len(orgs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = fleetOrg(read, orgs[i])
			}
		}()
	}
	for i := range orgs {
		next <- i
	}
	close(next)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return &client.FleetSubscriptions{Orgs: out}, nil
}

// fleetOrg is one org's subscriptions in the fleet read. An org reached after the
// caller's deadline is not read and says so.
func fleetOrg(ctx context.Context, o *organization.Organization) client.OrgSubscriptions {
	row := client.OrgSubscriptions{Org: o.Name, Rows: []client.Subscription{}}
	if err := ctx.Err(); err != nil {
		row.Error = err.Error()
		return row
	}
	rows, err := subscriptionsOf(ctx, o, "", "")
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Rows = rows
	return row
}
