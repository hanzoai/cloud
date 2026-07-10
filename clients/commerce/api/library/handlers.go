package library

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)
	namespaced := middleware.Namespace()

	api := router.Group("library")

	api.POST("/shopjs", publishedRequired, namespaced, LoadShopJS)
	api.POST("/coinjs", publishedRequired, namespaced, LoadShopJS)
	api.POST("/daisho", LoadDaisho)
}
