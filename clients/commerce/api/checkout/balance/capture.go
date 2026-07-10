package balance

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

var FailedToCaptureCharge = errors.New("Failed to capture charge")

func Capture(org *organization.Organization, ord *order.Order) (*order.Order, []*payment.Payment, error) {
	db := ord.Datastore()

	// Get payments for this order
	payments := make([]*payment.Payment, 0)
	if _, err := payment.Query(db).Ancestor(ord.Key()).GetAll(&payments); err != nil {
		return nil, payments, err
	}

	log.Debug("payments %v", payments)

	// Capture any uncaptured payments
	for _, p := range payments {
		if !p.Captured {
			// Update payment
			p.Captured = true
			p.Status = payment.Paid
			p.Init(db)
			p.Put()

			trans := transaction.New(db)
			trans.DestinationId = ord.UserId
			trans.Amount = p.Amount
			trans.Currency = p.Currency
			trans.Type = transaction.Withdraw
			trans.Test = ord.Test
			trans.SourceId = ord.Id()
			trans.SourceKind = "order"
			trans.Put()
		}
	}

	return ord, payments, nil
}
