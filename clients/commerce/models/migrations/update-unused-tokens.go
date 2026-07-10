package migrations

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/token"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("update-unused-tokens",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")

		return NoArgs
	},
	func(db *ds.Datastore, tok *token.Token) {
		if !tok.Used && tok.Expired() {
			now := time.Now()
			tok.Used = false
			tok.Expires = now.Add(30 * 24 * time.Hour)
			if err := tok.Put(); err != nil {
				log.Error(err, db.Context)
			}
		}
	},
)
