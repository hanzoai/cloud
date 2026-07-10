package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("reset-refund-counters",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "stoned")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		ctx := db.Context
		if ord.Refunded != ord.Total {
			return
		}
		if ord.StoreId != "" {
			if err := counter.IncrementByAll(ctx, "order.refund.count", ord.StoreId, ord.ShippingAddress.Country, 1, ord.UpdatedAt); err != nil {
				return
			}
		}
		if err := counter.IncrementByAll(ctx, "order.refund.count", "", ord.ShippingAddress.Country, 1, ord.UpdatedAt); err != nil {
			return
		}
	},
)
