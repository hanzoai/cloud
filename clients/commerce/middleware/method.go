package middleware

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

// HeaderMethodOverride is a commonly used
// Http header to override the method.
const HeaderMethodOverride = "X-HTTP-Method-Override"

// ParamMethodOverride is a commonly used
// HTML form parameter to override the method.
const ParamMethodOverride = "_method"

var HttpMethods = []string{"PUT", "PATCH", "DELETE"}

// ErrInvalidOverrideMethod is returned when
// an invalid http method was given to OverrideRequestMethod.
var ErrInvalidOverrideMethod = errors.New("invalid override method")

func IsValidMethodOverride(method string) bool {
	for _, m := range HttpMethods {
		if m == method {
			return true
		}
	}
	return false
}

// OverrideRequestMethod overrides the http
// request's method with the specified method.
func OverrideRequestMethod(c *gin.Context, method string) error {
	c.Request.Header.Set(HeaderMethodOverride, method)
	c.Request.Method = method
	return nil
}

func MethodOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only override POST methods
		if c.Request.Method != "POST" {
			return
		}

		// Try to override method using query
		m := c.Request.FormValue(ParamMethodOverride)
		if IsValidMethodOverride(m) {
			OverrideRequestMethod(c, m)
		}

		// Try to override method using header
		m = c.Request.Header.Get(HeaderMethodOverride)
		if IsValidMethodOverride(m) {
			OverrideRequestMethod(c, m)
		}

		if c.Request.Method != "POST" {
			log.Warn("Method overriden to %v", c.Request.Method)
		}
	}
}
