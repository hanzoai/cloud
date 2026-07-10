package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("damon-mailchimp-store",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "damon")

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", "damon").Get(); err != nil {
			panic(err)
		}
		return []interface{}{org.Mailchimp.APIKey, org.DefaultStore}
	},
	func(db *ds.Datastore, stor *store.Store, apiKey, defaultStore string) {
		if apiKey == "" {
			log.Warn("No MailChimp API Key", db.Context)
			return
		}

		mc := integration.Mailchimp{
			APIKey: apiKey,
		}
		client := mailchimp.New(db.Context, mc)

		if err := client.CreateStore(stor); err != nil {
			log.Error("Failed to create Mailchimp store: %v", err, db.Context)
		}
	},
)
