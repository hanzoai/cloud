package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("set-userid-for-referrals",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")
		return NoArgs
	},
	func(db *ds.Datastore, refl *referral.Referral) {
		ctx := db.Context
		orderId := refl.OrderId

		// Look up order referenced by referral
		ord := order.New(db)
		if err := ord.GetById(orderId); err != nil {
			log.Error("Failed to query for order: %v\n%v", orderId, err, ctx)
			return
		}

		// Same user, just return
		if ord.UserId == refl.UserId {
			log.Debug("Same user")
			return
		}

		// Update the user id on the referrer
		refl.UserId = ord.UserId
		refl.Put()
	},
)
