package shipstation

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipstation/export"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipstation/shipnotify"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func setOrg(c *gin.Context) {
	db := datastore.New(c)
	org := organization.New(db)
	if err := org.GetById(c.Params.ByName("organization")); err != nil {
		log.Panic("Organization not specified", c)
	}

	userid, err := auth.GetCurrentUserId(c)
	if err != nil {
		log.Panic("Unable to get current user", c)
	}

	if !(org.IsAdmin(userid) || org.IsOwner(userid)) {
		log.Panic("Not a valid admin/owner for this organization", c)
	}

	c.Set("organization", org)
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("shipstation")

	basicAuth := middleware.BasicAuth()

	api.GET("/:organization", basicAuth, setOrg, export.Export)
	api.POST("/:organization", basicAuth, setOrg, shipnotify.ShipNotify)
}
