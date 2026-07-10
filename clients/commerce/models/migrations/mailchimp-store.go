package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("mailchimp-store",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, org *organization.Organization) {
		if org.Mailchimp.APIKey == "" {
			log.Warn("No MailChimp API Key for %s", org.Name, db.Context)
			return
		}

		if org.Mailchimp.ListId == "" {
			log.Warn("No ListId for %s", org.Name, db.Context)
			return
		}

		client := mailchimp.New(db.Context, org.Mailchimp)

		if org.DefaultStore == "" {
			log.Warn("Default Store does not exist for %s", db.Context)
			if org.Currency == "" {
				org.Currency = currency.USD
			}

			nsdb := datastore.New(org.Namespaced(db.Context))

			// Create new store
			stor := store.New(nsdb)
			stor.Name = "default"
			stor.GetOrCreate("Name=", stor.Name)
			stor.Prefix = "/"
			stor.Currency = org.Currency
			stor.Mailchimp.APIKey = org.Mailchimp.APIKey
			stor.Mailchimp.ListId = org.Mailchimp.ListId
			stor.MustUpdate()

			org.DefaultStore = stor.Id()

			if err := client.CreateStore(stor); err != nil {
				log.Error("Failed to create Mailchimp store: %v", err, db.Context)
			} else {
				org.MustUpdate()
			}
		} else {
			log.Warn("Default Store exists for %s", db.Context)
		}
	},
)
