package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/util/template"
)

var template503 = `
<html>
	<head>
		<style>
			body {
				font-family:monospace;
				margin:20px;
			}
		</style>
	</head>
	<body>
		<h4>503 Service Unavailable </h4>
		<p>Service termporarily unvailable.</p>
	</body>
</html>
`

// Serve custom 503 page.
func UnavailableHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.AbortWithError(http.StatusServiceUnavailable, errors.New("Service temporarily unavailable."))

		c.Next()

		if config.IsDevelopment {
			c.Writer.Write([]byte(template503))
		} else {
			template.Render(c, "error/503.html")
		}
	}
}
