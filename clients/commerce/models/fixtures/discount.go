package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/rule"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/scope"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/target"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var Discount = New("discount", func(c *gin.Context) *discount.Discount {
	// Get namespaced db
	db := getNamespaceDb(c)

	// Batman shirt
	prod := product.New(db)
	prod.Slug = "batman"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.Name = "Batman T-shirt"
	prod.Headline = "Batman."
	prod.Description = "It's a batman t-shirt."
	prod.Options = []*product.Option{
		&product.Option{
			Name:   "Size",
			Values: []string{"Batwing"},
		},
		&product.Option{
			Name:   "Size",
			Values: []string{"Batmobile"},
		},
	}
	prod.Price = 9900
	prod.Currency = currency.USD
	prod.MustPut()

	// Create discount rules for ludela
	dis := discount.New(db)
	dis.Name = "Bulk Discount"
	dis.Type = discount.Bulk
	dis.GetOrCreate("Name=", dis.Name)
	dis.Scope.Type = scope.Organization
	dis.Target.Type = target.Product
	dis.Target.ProductId = prod.Id()

	rule1 := discount.Rule{
		Trigger: rule.Trigger{
			Quantity: rule.Quantity{
				Start: 2,
			},
		},
		Action: rule.Action{
			Discount: rule.Discount{
				Flat: 5,
			},
		},
	}

	rule2 := discount.Rule{}
	rule2.Trigger.Quantity.Start = 3
	rule2.Action.Discount.Flat = 16

	dis.Rules = []discount.Rule{rule1, rule2}
	dis.MustUpdate()

	return dis
})
