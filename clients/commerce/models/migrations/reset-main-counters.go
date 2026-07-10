package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/return"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

func MustNukeCounter(db *ds.Datastore, tag string) {
	var ks []iface.Key
	var err error
	ks, err = db.Query(counter.ShardKind).Filter("Tag=", tag).Limit(500).KeysOnly().GetAll(nil)
	if err != nil {
		log.Panic("Cannot delete %s, %v", tag, err, db.Context)
	}
	for len(ks) != 0 {
		db.MustDeleteMulti(ks)
		ks, err = db.Query(counter.ShardKind).Filter("Tag=", tag).Limit(500).KeysOnly().GetAll(nil)
		if err != nil {
			log.Panic("Cannot delete %s, %v", tag, err, db.Context)
		}
	}
}

var _ = New("reset-main-counters",
	func(c *gin.Context) []interface{} {
		orgName := "kanoa"

		c.Set("namespace", orgName)

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", orgName).Get(); err != nil {
			panic(err)
		}

		nsDb := ds.New(org.Namespaced(c))
		MustNukeCounter(nsDb, "user.count")
		MustNukeCounter(nsDb, "subscriber.count")
		MustNukeCounter(nsDb, "order.count")
		MustNukeCounter(nsDb, "order.refunded")

		MustNukeCounter(nsDb, "order.revenue")
		MustNukeCounter(nsDb, "order.refunded.amount")
		MustNukeCounter(nsDb, "order.refunded.count")
		MustNukeCounter(nsDb, "order.returned.count")
		MustNukeCounter(nsDb, "order.shipped.cost")
		MustNukeCounter(nsDb, "order.shipped.count")

		prods := make([]*product.Product, 0)
		if _, err := product.Query(nsDb).GetAll(&prods); err != nil {
			panic(err)
		}

		for _, prod := range prods {
			MustNukeCounter(nsDb, "product."+prod.Id()+".inventory.cost")
			MustNukeCounter(nsDb, "product."+prod.Id()+".sold")
			MustNukeCounter(nsDb, "product."+prod.Id()+".revenue")
			MustNukeCounter(nsDb, "product."+prod.Id()+".refunded.count")
			MustNukeCounter(nsDb, "product."+prod.Id()+".returned.count")
			MustNukeCounter(nsDb, "product."+prod.Id()+".shipped.count")
		}

		return NoArgs
	},
	func(db *ds.Datastore, usr *user.User) {
		if err := counter.IncrUser(db.Context, usr.CreatedAt); err != nil {
			log.Error("IncrUser Error %v", err, db.Context)
		}
	},
	func(db *ds.Datastore, usr *subscriber.Subscriber) {
		if err := counter.IncrSubscriber(db.Context, usr.CreatedAt); err != nil {
			log.Error("IncrSubscriber Error %v", err, db.Context)
		}
	},
	func(db *ds.Datastore, rtn *return_.Return) {
		items := rtn.Items
		if len(items) == 0 {
			ord := order.New(db)
			if err := ord.GetById(rtn.OrderId); err != nil {
				log.Error("Could not get order %v", err, db.Context)
				return
			}
			items = ord.Items
		}
		if err := counter.IncrOrderReturn(db.Context, items, rtn); err != nil {
			log.Error("IncrOrderReturn Error %v", err, db.Context)
		}
	},
	func(db *ds.Datastore, ord *order.Order) {
		if err := counter.IncrOrder(db.Context, ord); err != nil {
			log.Error("IncrOrder Error %v", err, db.Context)
		}
		if err := counter.IncrOrderRefund(db.Context, ord, int(ord.Refunded), ord.UpdatedAt); err != nil {
			log.Error("IncrOrderRefund Error %v", err, db.Context)
		}
		if err := counter.IncrOrderShip(db.Context, ord, ord.UpdatedAt); err != nil {
			log.Error("IncrOrderShipped Error %v", err, db.Context)
		}
	},
)
