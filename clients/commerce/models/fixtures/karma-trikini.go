package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("karma-trikini", func(c *gin.Context) []*product.Product {
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

	BIKINI_STYLES := []size{
		size{
			name: "Railay Bikini in trippy leopard",
			id:   "railay-tl",
		},
		size{
			name: "Railay Bikini in dragon blossom",
			id:   "railay-db",
		},
		size{
			name: "Ruby Bikini in trippy leopard",
			id:   "ruby-tl",
		},
		size{
			name: "Ruby Bikini in dragon blossom",
			id:   "ruby-db",
		},
		size{
			name: "Lafayette Bikini in trippy leopard",
			id:   "lafayette-tl",
		},
		size{
			name: "Lafayette Bikini in dragon blossom",
			id:   "lafayette-db",
		},
		size{
			name: "Bikini-n-Chill Bikini in trippy leopard",
			id:   "bikini-n-chill-tl",
		},
		size{
			name: "Bikini-n-Chill Bikini in dragon blossom",
			id:   "bikini-n-chill-db",
		},
	}

	prods := []*product.Product{}

	for _, s1 := range BIKINI_STYLES {
		for _, s2 := range SIZES {
			for _, s3 := range SIZES {
				prod := product.New(nsdb)
				prod.Slug = "trikini-" + s1.id + "-" + s2.id + "-" + s3.id
				prod.GetOrCreate("Slug=", prod.Slug)
				prod.Name = "Trikini " + s1.name + " " + s2.name + " Top " + s3.name + " Bottom"
				prod.Description = "Guess what it’s 2020 and the only way to look cute and safe at the beach is with your bikini and mask, a.k.a. the tri-kini matching set. Choose a bikini style from the Less Boring Summer Collection and any mask. Available in our Trippy Leopard print/Dragon Blossom print. "
				prod.Currency = currency.USD
				prod.ListPrice = currency.Cents(23500)
				prod.Price = currency.Cents(23500)
				prod.Update()

				prods = append(prods, prod)
			}
		}
	}

	return prods
})
