package platform

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/cron/payout"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/delay"
)

// Create a copy payout.TransferFee delay.Func configured to use unique queue
var transferFee = payout.TransferFee.Queue("transfer-platform-fee")

// Create transfers for all un-transferred fees for associated organization
var transferFees = delay.Func("transfer-platform-fees", func(ctx context.Context, orgId string) {
	db := datastore.New(ctx)

	// Fetch organization
	org := organization.New(db)
	if err := org.GetById(orgId); err != nil {
		log.Error("Failed to fetch organization '%s': %v", orgId, err, ctx)
		return
	}

	log.Debug("Fetching platform fees for organization: %s", org.Name, ctx)

	nsctx := org.Namespaced(ctx)
	db = datastore.New(nsctx)
	q := fee.Query(db).Filter("Type=", fee.Platform).Filter("TransferId=", "").Filter("Status=", fee.Payable).KeysOnly()
	t := q.Run()

	// Loop over entities passing them into workerFunc one at a time
	for {
		key, err := t.Next(nil)

		// Done iterating
		if err == datastore.Done {
			break
		}

		// Skip field mismatch errors
		if err = datastore.IgnoreFieldMismatch(err); err != nil {
			log.Error("Failed to fetch next entity: %v", err, ctx)
			break
		}

		// Create transfer for associated fee. Note: uses datastore-encoded key
		// to identify fee rather than our hashid.
		transferFee.Call(ctx, "", org.Name, key.Encode())
	}
})

// Payout fees for all transfers
func Payout(ctx context.Context) error {
	db := datastore.New(ctx)

	// FIXME: Use iteration instead
	log.Debug("Fetching all organizations", ctx)
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(db).GetAll(&orgs); err != nil {
		log.Error("Failed to fetch organizations", ctx)
		return err
	}

	// Transfer fees for each organization
	for _, org := range orgs {
		log.Debug("Processing platform fees for organization: %s", org.Name, ctx)
		transferFees.Call(ctx, org.Id())
	}

	return nil
}
