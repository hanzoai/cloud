package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var Product = New("product", func(c *gin.Context) *product.Product {
	// Get namespaced db
	db := getNamespaceDb(c)

	// Doge shirt
	prod := product.New(db)
	prod.Slug = "doge-shirt"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.Name = "Such T-shirt"
	prod.Headline = "wow  such shirt  much tee"
	prod.Description = `wow
	　　　　　　such shirt
	much tee

			nice shop

	　so hip

	　　　　so doge
	`
	prod.Options = []*product.Option{
		&product.Option{
			Name:   "Size",
			Values: []string{"Much", "Wow"},
		},
	}
	prod.Price = 2000
	prod.Currency = currency.USD
	prod.MustPut()

	// Sad Keanu shirt
	prod = product.New(db)
	prod.Slug = "sad-keanu-shirt"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.Name = "Sad Keanu T-shirt"
	prod.Headline = "Oh Keanu"
	prod.Description = "Sad Keanu is sad."
	prod.Options = []*product.Option{
		&product.Option{
			Name:   "Size",
			Values: []string{"Sadness"},
		},
	}
	prod.Price = 2500
	prod.Currency = currency.USD
	prod.MustPut()

	return prod
})
