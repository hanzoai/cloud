package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
)

var _ = New("add-missing-namespace", func(c *gin.Context) []interface{} {
	db := datastore.New(c)
	ns := namespace.New(db)
	ns.Name = "4050001"
	ns.IntId = 4050001
	err := ns.Put()
	if err != nil {
		log.Warn("Failed to put namespace: %v", err)
	}
	return NoArgs
})
