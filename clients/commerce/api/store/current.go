package store

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
)

// getCurrent returns the first (default) store for the authenticated org.
// The admin dashboard calls GET /store/current to resolve the active store context.
func getCurrent(c *gin.Context) {
	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)

	var s store.Store
	s.Init(db)

	q := s.Query().All().Limit(1)

	var stores []store.Store
	if _, err := q.GetAll(&stores); err != nil || len(stores) == 0 {
		// Return a minimal default so the dashboard can render
		c.JSON(http.StatusOK, gin.H{
			"store": gin.H{
				"id":               "default",
				"name":             "Default Store",
				"default_currency": "usd",
				"currencies":       []string{"usd"},
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"store": stores[0]})
}
