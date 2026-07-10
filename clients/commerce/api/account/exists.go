package account

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func exists(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	emailorusername := c.Params.ByName("emailorusername")

	usr := user.New(db)

	if err := usr.GetByEmail(emailorusername); err == nil {
		http.Render(c, 200, gin.H{"exists": true})
	} else if err := usr.GetByUsername(emailorusername); err == nil {
		http.Render(c, 200, gin.H{"exists": true})
	} else {
		http.Render(c, 200, gin.H{"exists": false})
	}
}
