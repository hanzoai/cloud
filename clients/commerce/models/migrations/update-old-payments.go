package migrations

import (
	"context"

	"github.com/gin-gonic/gin"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
)

var accessToken = ""

// Update charge in case order/pay id is missing in metadata
// (Legacy: Stripe calls removed)
func updateChargeFromPayment(ctx context.Context, pay *payment.Payment) error {
	log.Warn("updateChargeFromPayment: legacy Stripe migration skipped for payment %s", pay.Id(), ctx)
	return nil
}

// Ensure order has right payment id
func orderNeedsPaymentId(ctx context.Context, ord *order.Order, pay *payment.Payment) error {
	if len(ord.PaymentIds) > 0 && ord.PaymentIds[0] != pay.Id() {
		log.Warn("Payment '%v' not found in order '%v' PaymentIds: %#v", pay.Id(), ord.Id(), ord.PaymentIds, ctx)
		ord.PaymentIds = []string{pay.Id()}

		if err := ord.Put(); err != nil {
			log.Error("Failed to update order: %#v, bailing: %v", ord, err, ctx)
			return err
		}
	}

	return nil
}

func deletePayment(ctx context.Context, pay *payment.Payment) error {
	pay.Deleted = true
	if err := pay.Put(); err != nil {
		log.Error("Unable to mark payment '%s' as deleted: %v", pay.Id(), err, ctx)
		return err
	}
	return nil
}

var _ = New("update-old-payments",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		var payments []*payment.Payment

		// Bail out if we've been previously deleted
		if pay.Deleted {
			return
		}

		ctx := db.Context // Cache context

		// Query out payments with matching chargeId's, only one is linked to a
		// real order, and the charge should be pointed at that one.
		keys, err := payment.Query(db).Filter("Account.ChargeId=", pay.Account.ChargeId).GetAll(&payments)
		if err != nil {
			log.Error("Unable query for payments: %v", err, ctx)
			return
		}

		// Find newest/oldest payments
		oldest := pay
		var valid *payment.Payment
		var ord *order.Order

		for i, p := range payments {
			// Make sure we have a payment we can work with
			p.Init(db)

			// Find the oldest
			if p.CreatedAt.Before(oldest.CreatedAt) {
				oldest = p
			}

			// See if we have a valid order
			ord = order.New(db)
			if err := ord.GetById(p.OrderId); err != nil {
				// Not a good payment, no matching order
				deletePayment(ctx, p)
			} else {
				// Found a valid order, hooray!
				p.Parent = ord.Key()
				p.SetKey(keys[i])
				valid = p
			}
		}

		if valid == nil {
			log.Error("Unable to find a matching order for any payment: %#v", payments, ctx)
			return
		}

		valid.CreatedAt = oldest.CreatedAt
		valid.Buyer.UserId = ord.UserId
		valid.MustPut()

		// Update order if necessary
		if err := orderNeedsPaymentId(ctx, ord, valid); err != nil {
			return
		}

		log.Debug("Payment '%v' associated with order '%v'", valid.Id(), ord.Id(), ctx)
	},
)
