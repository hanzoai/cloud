package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("damon-order-projected-refund-counters",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "damon")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		if ord.Total != ord.Refunded {
			return
		}

		ctx := db.Context

		for _, item := range ord.Items {
			prod := product.New(ord.Datastore())
			if err := prod.GetById(item.ProductId); err != nil {
				log.Error("no product found %v", err, ctx)
			}
			for i := 0; i < item.Quantity; i++ {
				if err := counter.IncrementByAll(ctx, "order.projected.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
					log.Error("order.refunded error %v", err, db.Context)
				}
			}
		}
	},
)
