package migrations

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/token"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("create-tokens",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")
		return NoArgs
	},
	func(db *ds.Datastore, usr *user.User) {
		tok := token.New(usr.Datastore())
		tok.Email = usr.Email
		tok.UserId = usr.Id()
		tok.Expires = time.Now().Add(time.Hour * 168)
		tok.Put()
	},
)
