package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

var _ = New("wipe-search-documents",
	func(c *gin.Context) []interface{} {
		db := datastore.New(c)
		db.SetNamespace("damon")
		ctx := db.Context

		// Search functionality removed
		log.Info("wipe-search-documents: search functionality removed (no-op)", ctx)

		return NoArgs
	},
)
