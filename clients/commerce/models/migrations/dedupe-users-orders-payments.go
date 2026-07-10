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

func dedupeOrders(db *ds.Datastore, ord *order.Order, currentUsr, masterUsr *user.User) {
	ctx := db.Context

	// Same user, just return
	if masterUsr.Id() == currentUsr.Id() {
		log.Warn("Same user", ctx)
		return
	}

	// Need to update order
	log.Warn("Need to update order", ctx)

	// Update order with correct user id
	ord.UserId = masterUsr.Id()
	ord.Parent = masterUsr.Key()

	// Save references to old order
	oldkey := ord.Key()
	oldid := ord.Id()

	// Create a new key for this order
	newkey := ord.NewKey()
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

	ord.PaymentIds = make([]string, len(ord.PaymentIds))

	for i, pay := range payments {
		oldpaykey := pay.Key()
		oldpayid := pay.Id()

		pay.OrderId = ord.Id()
		pay.Parent = newkey

		// Create a new key for this order
		pay.NewKey()
		newpayid := pay.Id()

		log.Debug("Order: %v", ord, ctx)
		log.Debug("Old payment id: %v, new payment id: %v", oldpayid, newpayid, ctx)

		pay.Buyer.UserId = ord.UserId
		pay.Init(db)
		if err := pay.Put(); err != nil {
			log.Warn("Failed to update referral: %v", pay, err, ctx)
			return
		}

		// Delete old payment
		if err := db.Delete(oldpaykey); err != nil {
			log.Warn("Failed to delete old payment: %v", oldpaykey, err, ctx)
		}

		ord.PaymentIds[i] = newpayid
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
}

var _ = New("dedupe-users-orders-payments",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")
		return NoArgs
	},
	func(db *ds.Datastore, usr *user.User) {
		ctx := db.Context

		if strings.HasPrefix(usr.Email, "!______") {
			log.Warn("User deduped", ctx)
			return
		}

		// Try to find newest instance of a user with this email
		usr2 := user.New(db)
		if _, err := usr2.Query().Filter("Email=", usr.Email).Order("-CreatedAt").Get(); err != nil {
			log.Error("Failed to query for newest user: %v", err, ctx)
			return
		}

		// Same user, just return
		if usr2.Id() == usr.Id() {
			log.Warn("Same user", ctx)
			return
		}

		put := false

		// transfer accounts in case of shenanigans
		if usr2.Accounts.Stripe.CustomerId == "" && usr.Accounts.Stripe.CustomerId != "" {
			usr2.Accounts = usr.Accounts
			put = true
		}

		if string(usr.PasswordHash) != "" {
			usr2.PasswordHash = usr.PasswordHash
			put = true
		}

		if usr2.Enabled {
			usr2.Enabled = true
			put = true
		}

		if put {
			usr2.Put()
		}

		usr.Email = "!______" + usr.Email
		usr.Deleted = true
		usr.Put()

		// update all orders
		var ords []*order.Order
		if _, err := order.Query(db).Filter("UserId=", usr.Id()).GetAll(&ords); err != nil {
			log.Warn("Failed to query out Orders, UserId: %v", usr.Id(), err, ctx)
			return
		}

		for _, ord := range ords {
			dedupeOrders(db, ord, usr, usr2)
		}
	},
)
