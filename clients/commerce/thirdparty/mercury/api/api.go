package api

import (
	"github.com/gin-gonic/gin"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Route registers Mercury webhook endpoint.
func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("mercury")
	api.POST("/webhook", Webhook)
}
