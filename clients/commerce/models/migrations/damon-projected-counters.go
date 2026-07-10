package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("damon-projected-counters",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "damon")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		// if ord.Status == "cancelled" {
		// 	return
		// }

		ctx := db.Context

		projectedPrice := 0
		// Calculate Projected
		for _, item := range ord.Items {
			log.Warn("item %v", item.ProjectedPrice, db.Context)
			prod := product.New(ord.Datastore())
			if err := prod.GetById(item.ProductId); err == nil {
				projectedPrice += item.Quantity * int(prod.ProjectedPrice)
			}
		}

		if err := counter.IncrementByAll(ctx, "order.projected.revenue", ord.StoreId, ord.ShippingAddress.Country, projectedPrice, ord.CreatedAt); err != nil {
			log.Error("order.projected.revenue error %v", err, db.Context)
		}

		for _, item := range ord.Items {
			prod := product.New(ord.Datastore())
			if err := prod.GetById(item.ProductId); err != nil {
				log.Error("no product found %v", err, ctx)
			}
			for i := 0; i < item.Quantity; i++ {
				if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".projected.revenue", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
					log.Error("product."+prod.Id()+".projected.revenue error %v", err, db.Context)
				}
			}
		}
	},
)
