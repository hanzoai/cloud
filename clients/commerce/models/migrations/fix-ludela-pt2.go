package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("fix-ludela-pt2",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "ludela")
		return NoArgs
	},
	func(db *ds.Datastore, pay *payment.Payment) {
		key := pay.Key().Parent().Parent()
		usr := user.New(db)
		usr.Get(key)

		if usr.FirstName == "" {
			usr.FirstName = pay.Buyer.FirstName
		}

		if usr.LastName == "" {
			usr.LastName = pay.Buyer.LastName
		}

		usr.MustPut()
	},
)
