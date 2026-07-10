package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var _ = New("damon-users-mailchimp-refunded",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "damon")

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", "damon").Get(); err != nil {
			panic(err)
		}
		return []interface{}{org.Mailchimp.APIKey, org.Mailchimp.ListId, org.DefaultStore}
	},
	func(db *ds.Datastore, usr *user.User, apiKey, listId, defaultStore string) {
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

		ctx := db.Context

		if err := usr.LoadOrders(); err != nil {
			log.Error("loadorders error %v", err, ctx)
			return
		}

		paidOrders := 0
		for _, v := range usr.Orders {
			switch ps := v.PaymentStatus; ps {
			case payment.Paid:
				if !v.Test {
					paidOrders++
				}
			}
		}

		if paidOrders == 0 {
			// Determine store to use
			storeId := defaultStore

			stor := store.New(usr.Datastore())
			stor.MustGetById(storeId)

			// Subscribe user to list
			buy := Buyer{
				Email:     usr.Email,
				FirstName: usr.FirstName,
				LastName:  usr.LastName,
				Phone:     usr.Phone,
			}

			if err := client.UnsubscribeCustomer(stor.Mailchimp.ListId, buy); err != nil {
				log.Warn("Failed to delete Mailchimp customer - status: %v", err.Status, ctx)
				log.Warn("Failed to delete Mailchimp customer - unknown error: %v", err.Unknown, ctx)
				log.Warn("Failed to delete Mailchimp customer - mailchimp error: %v", err.Mailchimp, ctx)
			}
		}
	},
)
