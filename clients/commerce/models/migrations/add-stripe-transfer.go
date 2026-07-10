package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

// Legacy migration: Stripe charge lookups removed.
// This migration is historical and will no-op.
var _ = New("add-stripe-transfer",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "cycliq")
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		log.Debug("add-stripe-transfer: skipped (legacy Stripe migration) for payment %s", pay.Id(), db.Context)
	},
)
