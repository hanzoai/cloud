package middleware

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

func parseAuthHeader(fieldValue string) (string, string) {
	method, encoded := splitAuthorization(fieldValue)
	if method != "Basic" {
		return method, encoded
	}

	bytes, _ := base64.StdEncoding.DecodeString(encoded)
	credentials := strings.Split(string(bytes), ":")
	return credentials[0], credentials[1]
}

func BasicAuth() gin.HandlerFunc {
	realm := "Basic realm=" + strconv.Quote("Authorization Required")

	return func(c *gin.Context) {
		email, password := parseAuthHeader(c.Request.Header.Get("Authorization"))

		db := datastore.New(c)
		usr := user.New(db)
		if err := usr.GetByEmail(email); err != nil {
			c.Request.Header.Set("WWW-Authenticate", realm)
			c.AbortWithStatus(401)
			log.Warn("Unable to get user with email '%v': %v", email, err, c)
		}

		// Validate password
		if !usr.ComparePassword(password) {
			c.Request.Header.Set("WWW-Authenticate", realm)
			c.AbortWithStatus(401)
			log.Warn("Invalid password for user: %v", usr, c)
		}

		// Login user on session
		auth.Login(c, usr)
	}
}
