package middleware

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func AccountRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := GetToken(c)

		id := tok.UserId
		if id == "" {
			http.Fail(c, 403, "Access Denied", errors.New("Access Denied"))
			return
		}

		org := GetOrganization(c)
		db := datastore.New(org.Namespaced(c))
		u := user.New(db)

		if err := u.GetById(id); err != nil {
			http.Fail(c, 403, "Access Denied", errors.New("Access Denied"))
			return
		}

		c.Set("user", u)
	}
}

func GetUser(c *gin.Context) *user.User {
	return c.MustGet("user").(*user.User)
}
