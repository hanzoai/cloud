package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

// Cache the products
var products = make(map[string]string)

func GetProduct(db *ds.Datastore, id string) string {
	if slug, ok := products[id]; ok {
		return slug
	}

	prod := product.New(db)
	if err := prod.GetById(id); err != nil {
		log.Error(err, db.Context)
	}

	products[prod.Id()] = prod.Slug

	return prod.Slug
}

var _ = New("update-skus-on-orders",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		items := ord.Items

		update := false

		for i, item := range items {
			slug := GetProduct(db, item.ProductId)

			if item.ProductSlug != slug {
				update = true
				items[i].ProductSlug = slug
			}
		}

		if update {
			if err := ord.Put(); err != nil {
				log.Error(err, db.Context)
			}
		}
	},
)
