package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var _ = New("damon-sample-orders", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "damon"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(db.Context))

	usr1 := user.New(nsdb)
	usr1.Email = "david.lam@damon.com"
	usr1.GetOrCreate("Email=", usr1.Email)

	// usr2 := user.New(nsdb)
	// usr2.GetByEmail("dtai@hanzo.ai")

	premierSlugs := []string{
		"HSP-BGL",
		"HSP-BRS",
		"HSP-GGP",
		"HSP-GRS",
		"HSP-GWP",
		"HSP-RWS",
		"HSP-WRW",
		"HSP-BGP",
		"HSP-BRW",
		"HSP-GGRS",
		"HSP-GRW",
		"HSP-RWL",
		"HSP-WGL",
		"HSP-BGRS",
		"HSP-GBRS",
		"HSP-GGS",
		"HSP-GRWL",
		"HSP-RWP",
		"HSP-WRRS",
		"HSP-BGW",
		"HSP-GBW",
		"HSP-GRP",
		"HSP-GWL",
		"HSP-RWRS",
		"HSP-WRS",
		"HSP-MS",
		"HSP-AS",
		"HS",
	}

	for _, s := range premierSlugs {
		ord1 := order.New(nsdb)
		ord1.UserId = usr1.Id()
		ord1.Currency = currency.USD
		ord1.Items = []lineitem.LineItem{
			lineitem.LineItem{
				ProductSlug: s,
				Quantity:    1,
			},
		}
		ord1.Test = true
		// ord1.UpdateAndTally(nil)
		ord1.MustPut()

		// ord2 := order.New(nsdb)
		// ord2.UserId = usr2.Id()
		// ord2.Items = []lineitem.LineItem{
		// 	lineitem.LineItem{
		// 		ProductSlug: v,
		// 		Quantity:    1,
		// 	},
		// }
		// ord2.UpdateAndTally(nil)
		// ord2.MustPut()
	}

	return org
})
