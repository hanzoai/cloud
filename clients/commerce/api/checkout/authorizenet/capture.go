package authorizenet

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/authorizenet"
)

var FailedToCaptureCharge = errors.New("Failed to capture charge")
var NothingToCaptureError = errors.New("Nothing to Capture (Items or Subscriptions)")

func Capture(org *organization.Organization, ord *order.Order) (*order.Order, []*payment.Payment, error) {
	// Get namespaced context off order
	db := ord.Datastore()
	ctx := db.Context

	// Get payments for this order
	payments := make([]*payment.Payment, 0)
	if err := payment.Query(db).Ancestor(ord.Key()).GetModels(&payments); err != nil {
		return nil, payments, err
	}

	log.Debug("payments %v", payments)

	// Get client we can use for API calls
	con := org.AuthorizeNetToken(!org.Live)

	loginId := con.LoginId
	transactionKey := con.TransactionKey
	key := con.Key

	client := authorizenet.New(ctx, loginId, transactionKey, key, !org.Live)

	if ord.Total > 0 {
		// Capture any uncaptured payments
		for _, p := range payments {

			if !p.Captured {
				p2, err := client.Capture(p)

				// Charge failed for some reason, bail
				if err != nil {
					return ord, payments, err
				}
				if !p2.Captured {
					return ord, payments, FailedToCaptureCharge
				}

				// Update payment
				p2.Captured = true
				// p.Amount = currency.Cents(ord.Amount)
				// p.AmountRefunded = currency.Cents(p2ch.AmountRefunded)
				// p.Account.BalanceTransactionId = ch.Tx.ID
				// p.AmountTransferred = currency.Cents(ch.Tx.Amount)
				// p.CurrencyTransferred = currency.Type(ch.Tx.Currency)
			}
		}
	} else if len(ord.Subscriptions) == 0 {
		return ord, payments, NothingToCaptureError
	}

	return ord, payments, nil
}
