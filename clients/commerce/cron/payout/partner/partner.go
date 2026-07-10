package partner

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/cron/payout"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/partner"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Create a copy payout.TransferFee delay.Func configured to use unique queue
var transferFee = payout.TransferFee.Queue("transfer-partner-fee")

// Create transfers for all un-transferred fees for associated partner
var transferFees = delay.Func("transfer-partner-fees", func(ctx context.Context, namespace, partnerId string, cutoff time.Time) {
	db := datastore.New(ctx)

	// Fetch partner
	par := partner.New(db)
	if err := par.GetById(partnerId); err != nil {
		log.Error("Failed to fetch partner '%s': %v", partnerId, err, ctx)
		return
	}

	log.Debug("Transferring partner fees collected in '%s'", namespace, ctx)

	// Switch namespace using context
	nsctx := nscontext.WithNamespace(ctx, namespace)

	// Iterate over fees that have not been transfered
	db = datastore.New(nsctx)
	q := fee.Query(db).Ancestor(par.Key()).Filter("TransferId=", "").Filter("Status=", fee.Payable).Filter("CreatedAt<", cutoff).KeysOnly()
	t := q.Run()

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

		// Create transfer for associated fee
		transferFee.Call(ctx, "", namespace, key.Encode())
	}
})

// Payout partners
func Payout(ctx context.Context) error {
	db := datastore.New(ctx)

	log.Debug("Fetching all organizations", ctx)
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(db).GetAll(&orgs); err != nil {
		log.Error("Failed to fetch organizations: %v", err, ctx)
		return err
	}

	// Transfer fees for each organization
	for _, org := range orgs {
		if len(org.Partners) == 0 {
			continue
		}

		log.Debug("Processing partner fees for organization: %s", org.Name, ctx)
		for _, p := range org.Partners {
			// Fetch partner
			par := partner.New(db)
			if err := par.GetById(p.Id); err != nil {
				log.Error("Failed to get partner '%s': %v", p.Id, err, ctx)
				continue
			}

			// Skip partners without a payout access token
			if par.Stripe.AccessToken == "" {
				continue
			}

			log.Debug("Processing partner fees for organization: '%s'", org.Name, ctx)
			transferFees.Call(ctx, org.Name, par.Id(), par.Schedule.Cutoff(time.Now()))
		}
	}

	return nil
}
