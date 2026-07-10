package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("fix-update-old-payments-pt-2",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		if pay.Deleted || pay.Test {
			return
		}

		// Legacy: Stripe charge update removed
		log.Debug("fix-update-old-payments-pt-2: skipped (legacy Stripe migration) for payment %s", pay.Id(), db.Context)
	},
)
