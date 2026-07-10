package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("karma-products-trunk-show", func(c *gin.Context) []*product.Product {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "karma").Get()

	nsdb := datastore.New(org.Namespaced(db.Context))

	SIZES := []size{
		size{
			id:   "XS",
			name: "XS",
		},
		size{
			id:   "S",
			name: "S",
		},
		size{
			id:   "M",
			name: "M",
		},
		size{
			id:   "L",
			name: "L",
		},
		size{
			id:   "XL",
			name: "XL",
		},
		size{
			id:   "XXL",
			name: "XXL",
		},
	}

	prods := []*product.Product{}

	for _, s1 := range SIZES {
		for _, s2 := range SIZES {
			prod := product.New(nsdb)
			prod.Slug = "trunk-show-" + s1.id + "-" + s2.id
			prod.GetOrCreate("Slug=", prod.Slug)
			prod.Name = "Trunk Show " + s1.name + " Top " + s2.name + " Bottom"
			prod.Description = "For women that will elevate our brand through createive content creation + #bikinisthatsavetheplanet. We set up the store and mage the fulfillment, while you make 20% of every sale. Introducing YOU X KARMA"
			prod.Currency = currency.USD
			prod.ListPrice = currency.Cents(40000)
			prod.Price = currency.Cents(40000)
			prod.Update()

			prods = append(prods, prod)
		}
	}

	return prods
})
