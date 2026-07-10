package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("add-namespace-for-orgs",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "")
		return NoArgs
	},
	func(db *ds.Datastore, org *organization.Organization) {
		ns := namespace.New(db)
		ns.Name = org.Name
		ns.IntId = org.Key().IntID()
		err := ns.Put()
		if err != nil {
			log.Warn("Failed to put namespace: %v", err)
		}
	},
)
