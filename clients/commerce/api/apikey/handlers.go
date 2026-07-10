package apikey

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/apipermission"
	"github.com/hanzoai/cloud/clients/commerce/models/publishableapikey"
	"github.com/hanzoai/cloud/clients/commerce/models/role"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(publishableapikey.PublishableApiKey{})
	api.POST("/:publishableapikeyid/revoke", namespaced, Revoke)
	api.Route(router, args...)

	// RBAC CRUD
	rest.New(role.Role{}).Route(router, args...)
	rest.New(apipermission.ApiPermission{}).Route(router, args...)
}

// Revoke marks an API key as revoked by setting RevokedAt to the current time.
func Revoke(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("publishableapikeyid")

	k := publishableapikey.New(db)
	if err := k.GetById(id); err != nil {
		http.Fail(c, 404, "No API key found with id: "+id, err)
		return
	}

	now := time.Now()
	k.RevokedAt = &now

	// Set revokedBy from current user if available
	if u, exists := c.Get("user"); exists && u != nil {
		k.RevokedBy = middleware.GetUser(c).Id()
	}

	if err := k.Update(); err != nil {
		http.Fail(c, 500, "Failed to revoke API key", err)
		return
	}

	http.Render(c, 200, k)
}
