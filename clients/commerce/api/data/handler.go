package data

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	namespaced := middleware.Namespace()

	api := router.Group("/c/data")
	api.Use(middleware.AccessControl("*"))

	api.GET("/dashboard/:period/:year/:month/:day/:tzOffset", adminRequired, namespaced, dashboard)
}
