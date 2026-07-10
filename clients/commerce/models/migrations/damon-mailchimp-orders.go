package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("damon-mailchimp-orders",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "damon")

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", "damon").Get(); err != nil {
			panic(err)
		}
		return []interface{}{org.Mailchimp.APIKey, org.Mailchimp.ListId, org.DefaultStore}
	},
	func(db *ds.Datastore, ord *order.Order, apiKey, listId, defaultStore string) {
		if apiKey == "" {
			log.Warn("No MailChimp API Key", db.Context)
			return
		}

		if defaultStore == "" {
			log.Warn("No Default Store", db.Context)
			return
		}

		if listId == "" {
			log.Warn("No ListId", db.Context)
			return
		}

		mc := integration.Mailchimp{
			APIKey: apiKey,
		}
		client := mailchimp.New(db.Context, mc)
		// Create order in mailchimp
		if err := client.CreateOrder(defaultStore, ord); err != nil {
			log.Warn("Failed to create Mailchimp order: %v", err, db.Context)
		} else if err := client.UpdateOrder(defaultStore, ord); err != nil {
			log.Warn("Failed to update Mailchimp order: %v", err, db.Context)
		}

		// Update cart
		car := cart.New(ord.Datastore())

		if ord.CartId != "" {
			if err := car.GetById(ord.CartId); err != nil {
				log.Warn("Unable to find cart: %v", err, db.Context)
			} else {
				// Delete cart in mailchimp
				if err := client.DeleteCart(defaultStore, car); err != nil {
					log.Warn("Failed to create Mailchimp cart: %v", err, db.Context)
				}
			}
		}

		pay := payment.New(db)

		if _, err := pay.Query().Filter("OrderId=", ord.Id()).Get(); err != nil {
			log.Warn("No Payment Found for %v: %v", ord.Id(), err, db.Context)
			return
		}

		// Just get buyer off first payment
		if err := client.SubscribeCustomer(listId, pay.Buyer, ""); err != nil {
			log.Warn("Failed to subscribe '%s' to Mailchimp list '%s': %v", pay.Buyer.Email, listId, err, db.Context)
		}
	},
)
