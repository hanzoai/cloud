package counter

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)

	namespaced := middleware.Namespace()
	origin := middleware.AccessControl("*")

	api := router.Group("counter")
	api.Use(origin)

	api.POST("", adminRequired, namespaced, search)
	api.POST("/dashboard/daily", adminRequired, namespaced, daily)
	api.GET("/product/:productid", publishedRequired, namespaced, searchProduct)
	api.GET("/topline", publishedRequired, namespaced, topLine)
}
