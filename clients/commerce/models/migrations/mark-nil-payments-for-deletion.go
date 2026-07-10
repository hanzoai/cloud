package migrations

import (
	"github.com/gin-gonic/gin"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
)

var _ = New("mark-nil-payments-for-deletion",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		if pay.Account.ChargeId == "" && pay.OrderId == "" && pay.Buyer.UserId == "" {
			pay.Deleted = true
			pay.Put()
			log.Warn("Nil payment found", db.Context)
		}
	})
