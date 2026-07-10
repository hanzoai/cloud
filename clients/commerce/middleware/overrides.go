package middleware

import (
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// Try and detect verbose flag set on request, we only log DEBUG level in
// production if verbose=1 is added as a query param.
func DetectVerbose(query *url.Values) bool {
	// We check for both v=true or verbose=true
	param := strings.ToLower(query.Get("v"))
	if param == "" {
		param = query.Get("verbose")
	}

	if param != "" && (param == "1" || param == "true") {
		return true
	}

	return false
}

func DetectTest(query *url.Values) bool {
	param := strings.ToLower(query.Get("test"))

	if param != "" && (param == "1" || param == "true") {
		return true
	}

	return false
}

// Check query for special config override params and update session.
func DetectOverrides() gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()
		c.Set("verbose", DetectVerbose(&query))
		c.Set("test", DetectTest(&query))
	}
}
