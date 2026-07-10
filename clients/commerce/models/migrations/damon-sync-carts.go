package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("damon-sync-carts",
	func(c *gin.Context) []interface{} {
		db := ds.New(c)
		org := organization.New(db)
		org.GetById("damon")
		c.Set("namespace", "damon")
		return []interface{}{org.DefaultStore, org.Mailchimp.APIKey}
	},
	func(db *ds.Datastore, car *cart.Cart, defaultStore, apiKey string) {
		// Don't add carts which have converted into orders
		if car.OrderId != "" {
			return
		}

		mc := integration.Mailchimp{
			APIKey: apiKey,
		}
		// Update Mailchimp cart
		if car.UserId != "" || car.Email != "" {
			client := mailchimp.New(db.Context, mc)
			client.UpdateOrCreateCart(defaultStore, car)
		}
	},
)
