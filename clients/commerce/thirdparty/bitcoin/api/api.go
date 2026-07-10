package api

import (
	"github.com/gin-gonic/gin"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Wire up Bitcoin endpoint
func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("bitcoin")
	api.POST("/webhook", Webhook)
}
