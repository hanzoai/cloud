package middleware

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// Serve custom 404 page.
func NotFoundHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if !c.Writer.Written() && c.Writer.Status() == 404 {
			c.Writer.Header().Set("Content-Type", "text/html")
			c.Writer.WriteHeader(http.StatusNotFound)

			// Simple 404 response (can be enhanced with template support)
			if os.Getenv("ENV") == "development" {
				c.Writer.Write([]byte("<head><style>body{font-family:monospace; margin:20px}</style><h4>404 Not Found </h1><p>No such file or directory.</p>"))
			} else {
				c.Writer.Write([]byte("<head><style>body{font-family:monospace; margin:20px}</style><h4>404 Not Found</h1><p>No such file or directory.</p>"))
			}
		}
	}
}
