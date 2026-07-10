package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("dedupe-payments-2",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		// Delete payments which have been marked for deletion
		if pay.Deleted {
			if err := pay.Delete(); err != nil {
				log.Error("Failed to delete payment '%s': %v", pay.Id(), err, db.Context)
			}
		}
	},
)
