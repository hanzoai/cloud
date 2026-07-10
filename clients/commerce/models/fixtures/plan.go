package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/deprecated/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var Plan = New("plan", func(c *gin.Context) *plan.Plan {
	// Get namespaced db
	db := getNamespaceDb(c)

	// Doge shirt
	pln := plan.New(db)
	pln.Slug = "much-shirts"
	pln.GetOrCreate("Slug=", pln.Slug)
	pln.Name = "Much Monthly Shirt"
	pln.Description = `wow
	　　　　　　such shirt
	much tee

			nice shop

	　so hip

	　　　　so doge
	`
	pln.Price = 2000
	pln.Currency = currency.USD
	pln.Interval = Monthly
	pln.IntervalCount = 1
	// manually configured in test environment

	pln.MustPut()

	return pln
})
