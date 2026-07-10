package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

// Legacy migration: Stripe refund calls removed.
// This migration is historical and will no-op.
var _ = New("kanoa-batch2-refund",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		log.Debug("kanoa-batch2-refund: skipped (legacy Stripe migration) for order %s", ord.Id(), db.Context)
	},
)
