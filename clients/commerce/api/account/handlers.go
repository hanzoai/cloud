package account

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)
	accountRequired := middleware.AccountRequired()
	namespaced := middleware.Namespace()

	api := router.Group("account")
	api.Use(publishedRequired)

	api.GET("", accountRequired, namespaced, get)
	api.PUT("", accountRequired, namespaced, update)
	api.PATCH("", accountRequired, namespaced, patch)

	api.GET("/order/:orderid", accountRequired, namespaced, getOrder)
	api.PATCH("/order/:orderid", accountRequired, namespaced, patchOrder)
	api.POST("/withdraw", accountRequired, namespaced, withdraw)
	api.POST("/paymentmethod/:paymentmethodtype", accountRequired, namespaced, createPaymentMethod)

	api.GET("/exists/:emailorusername", namespaced, exists)

	api.POST("/login", namespaced, login)

	api.POST("/create", namespaced, create)
	api.POST("/enable/:tokenid", namespaced, enable)

	api.POST("/reset", namespaced, reset)
	api.POST("/confirm/:tokenid", namespaced, confirm)
}
