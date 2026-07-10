package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

func AcquireUser(moduleName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if u, err := auth.GetCurrentUser(c); err != nil {
			log.Warn("Unable to acquire user.", c)
			session.Clear(c)
			c.Redirect(302, config.UrlFor(moduleName, "/login"))
			c.AbortWithStatus(302)
		} else {
			c.Set("user", u)
		}
	}
}

func GetCurrentUser(c *gin.Context) *user.User {
	return c.MustGet("user").(*user.User)
}
