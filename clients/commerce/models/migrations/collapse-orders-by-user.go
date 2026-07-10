package migrations

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("collapse-orders-by-user",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		ctx := db.Context
		userid := ord.UserId

		// Look up user for this order
		usr := user.New(db)
		if err := usr.GetById(userid); err != nil {
			log.Warn("Failed to query for user: %v", userid, ctx)
			return
		}

		// Try to find newest instance of a user with this email
		usr2 := user.New(db)
		if _, err := usr2.Query().Filter("Email=", strings.Replace(usr.Email, "!______", "", 1)).Order("-CreatedAt").Get(); err != nil {
			log.Error("Failed to query for newest user: %v", err, ctx)
			return
		}

		// Same user, just return
		if usr2.Id() == usr.Id() {
			log.Warn("Same user", ctx)
			return
		}

		// Need to update order
		log.Warn("Need to update order", ctx)

		// Update order with correct user id
		ord.UserId = usr2.Id()
		ord.Parent = usr2.Key()

		// Save references to old order
		oldkey := ord.Key()
		oldid := ord.Id()

		// Create a new key for this order
		ord.NewKey()
		newid := ord.Id()

		log.Debug("Order: %v", ord, ctx)
		log.Debug("Old order id: %v, new order id: %v", oldid, newid, ctx)

		// Update all references to old order id
		var referrers []*referrer.Referrer
		var referrals []*referral.Referral
		var payments []*payment.Payment

		if _, err := referrer.Query(db).Filter("OrderId=", oldid).GetAll(&referrers); err != nil {
			log.Warn("Failed to query out referrers, OrderId: %v", oldid, err, ctx)
			return
		}

		if _, err := referral.Query(db).Filter("OrderId=", oldid).GetAll(&referrals); err != nil {
			log.Warn("Failed to query out referrals, OrderId: %v", oldid, err, ctx)
			return
		}

		if _, err := payment.Query(db).Filter("OrderId=", oldid).GetAll(&payments); err != nil {
			log.Warn("Failed to query out payments, OrderId: %v", oldid, err, ctx)
			return
		}

		for _, ref := range referrers {
			ref.Init(db)
			if err := ref.Put(); err != nil {
				log.Warn("Failed to update referrer: %v", ref, err, ctx)
				return
			}
		}

		for _, refl := range referrals {
			refl.OrderId = ord.Id()
			refl.Init(db)
			if err := refl.Put(); err != nil {
				log.Warn("Failed to update referral: %v", refl, err, ctx)
				return
			}
		}

		for _, pay := range payments {
			pay.OrderId = ord.Id()
			pay.Buyer.UserId = ord.UserId
			pay.Init(db)
			if err := pay.Put(); err != nil {
				log.Warn("Failed to update referral: %v", pay, err, ctx)
				return
			}
		}

		// Delete old order
		if err := db.Delete(oldkey); err != nil {
			log.Warn("Failed to delete old order: %v", oldkey, err, ctx)
		}

		// Save order
		if err := ord.Put(); err != nil {
			log.Error("Failed to update order: %v", ord, err, ctx)
			return
		}
	},
)
