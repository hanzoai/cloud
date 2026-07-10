package payout

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/multi"
	"github.com/hanzoai/cloud/clients/commerce/models/partner"
	"github.com/hanzoai/cloud/clients/commerce/models/transfer"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

func transferFromFee(db *datastore.Datastore, fe *fee.Fee) *transfer.Transfer {
	tr := transfer.New(db)
	tr.Amount = fe.Amount
	tr.Currency = fe.Currency
	tr.FeeId = fe.Id()

	// Setup transfer
	switch fe.Type {
	case fee.Affiliate:
		aff := affiliate.New(db)
		aff.MustGetById(fe.AffiliateId)
		tr.Description = fmt.Sprintf("Affiliate transfer '%s'", tr.Id())
		tr.Destination = aff.Stripe.UserId
	case fee.Partner:
		par := partner.New(db)
		par.MustGetById(fe.PartnerId)
		tr.Description = fmt.Sprintf("Partner transfer '%s'", tr.Id())
		tr.Destination = par.Stripe.UserId
	case fee.Platform:
		tr.Description = fmt.Sprintf("Platform fee transfer '%s', fee '%s'", tr.Id(), fe.Id())
		tr.Destination = "" // External payout destination to be configured
	default:
		panic(fmt.Errorf("invalid fee type: '%s'", fe.Type))
	}
	return tr
}

// Create transfer for single fee
var TransferFee = delay.Func("transfer-fee", func(ctx context.Context, paymentToken, namespace, id string) {
	var fe *fee.Fee
	var tr *transfer.Transfer

	// Switch to correct namespace using context
	ctx = nscontext.WithNamespace(ctx, namespace)

	// Create transfer and update payment in transaction
	err := datastore.RunInTransaction(ctx, func(db *datastore.Datastore) error {
		// Fetch related fee
		fe = fee.New(db)
		if err := fe.GetById(id); err != nil {
			log.Warn("Failed to get fee with id '%s': %v", id, err, ctx)
			return err
		}

		// Deal with invalid states
		if fe.Status == fee.Disputed {
			return fmt.Errorf("fee '%s' is being disputed", fe.Id())
		}

		if fe.Status == fee.Transferred {
			return fmt.Errorf("fee '%s' is already transferred", fe.Id())
		}

		// Create associated transfer
		tr = transferFromFee(db, fe)

		// Allocate transfer ID and Update fee
		fe.Status = fee.Transferred
		fe.TransferId = tr.Id()

		// Save models
		models := []interface{}{tr, fe}
		return multi.Update(models)
	}, nil)
	// Bail out if error happened creating transactions, any changes in
	// transaction will have been rolled back.
	if err != nil {
		log.Warn("Failed to create transfer for fee '%s', transfer '%s': %v", fe.Id(), tr.Id(), err, ctx)
		return
	}

	// External payout (Square or manual) should be handled here.
	// Legacy Stripe payout removed.
	log.Warn("Transfer '%s' created for fee '%s' but external payout not yet implemented (legacy Stripe removed)", tr.Id(), fe.Id(), ctx)

	tr.Status = transfer.Pending
	if err := tr.Update(); err != nil {
		log.Error("Failed to update status of transfer '%s': %v\n%v", tr.Id(), err, tr, ctx)
	}
})
