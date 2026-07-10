package xd

import (
	"github.com/hanzoai/cloud/clients/commerce/util/router"
	"github.com/gin-gonic/gin"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("/xd")

	api.GET("/:domain/proxy.html", func(c *gin.Context) {
		c.Writer.WriteHeader(200)
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		domain := c.Params.ByName("domain")

		// Render response
		c.Writer.Write([]byte(`<!DOCTYPE HTML>
<script src="//cdn.rawgit.com/jpillora/xdomain/0.7.4/dist/xdomain.min.js" master="https://` + domain + `">
</script>`))
	})
}
