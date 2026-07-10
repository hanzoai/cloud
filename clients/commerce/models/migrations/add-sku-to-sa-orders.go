package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/order"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("add-sku-to-sa-orders",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "stoned")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		for i := range ord.Items {
			if ord.Items[i].ProductSlug == "earphone" {
				ord.Items[i].ProductSKU = "686696998137"
			}
		}
		db.Put(ord.Key(), ord)
	},
)
