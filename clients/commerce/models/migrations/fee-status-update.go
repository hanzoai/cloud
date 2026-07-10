package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

// Legacy migration: Stripe fee sync removed.
// Fee status updates are now handled by the payment processor abstraction.
var _ = New("payment-fee-status-update",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		log.Debug("payment-fee-status-update: skipped (legacy Stripe migration) for payment %s", pay.Id(), db.Context)
	},
)

var _ = New("fee-status-update",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, fe *fee.Fee) {
		log.Debug("fee-status-update: skipped (legacy Stripe migration) for fee %s", fe.Id(), db.Context)
	},
)
