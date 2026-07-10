package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var Store = New("store", func(c *gin.Context) *store.Store {
	// Get namespaced db
	db := getNamespaceDb(c)

	stor := store.New(db)
	stor.Slug = "suchtees"
	stor.GetOrCreate("Slug=", stor.Slug)

	stor.Name = "JPY Store"
	stor.Domain = "suchtees.com"
	stor.Prefix = "/"
	stor.Currency = currency.JPY
	stor.TaxNexus = []Address{Address{Line1: "123 Such St", City: "Tee City"}, Address{Line1: "456 Noo Ln", City: "Memetown"}}

	// Fetch first product
	prod := Product(c).(*product.Product)
	price := currency.Cents(30000)
	stor.Listings[prod.Id()] = store.Listing{
		ProductId: prod.Id(),
		Price:     &price,
	}

	stor.MustPut()
	return stor
})
