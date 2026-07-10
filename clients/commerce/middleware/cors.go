package middleware

import "github.com/gin-gonic/gin"

func AccessControl(allowOrigin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Set CORS headers for all requests.
		c.Writer.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")

		if c.Request.Method != "OPTIONS" {
			c.Next()
			return
		}

		// Handle preflight OPTIONS request
		reqHeaders := c.Request.Header.Get("Access-Control-Request-Headers")

		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		c.Writer.Header().Set("Access-Control-Max-Age", "86400")

		c.AbortWithStatus(204)
	}
}
