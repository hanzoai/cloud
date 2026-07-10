package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("fix-currency-set",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		ctx := db.Context
		ns, err := hashid.GetNamespace(db.Context, ord.Id())
		if err != nil {
			log.Warn("hash id decode error %v", err, ctx)
		}

		org := organization.New(db)
		org.Name = ns

		counter.AddCurrency(ctx, org, ord.Currency)
	},
)

var _ = New("load-counter-orders",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		var pays []*payment.Payment

		for _, pid := range ord.PaymentIds {
			pay := payment.New(db)
			if err := pay.GetById(pid); err != nil {
				break
			}

			pays = append(pays, pay)
		}

		ctx := db.Context
		ns, err := hashid.GetNamespace(db.Context, ord.Id())
		if err != nil {
			log.Warn("hash id decode error %v", err, ctx)
		}

		org := organization.New(db)
		org.Name = ns
		log.Debug("org name is %v", ns)

		t := ord.CreatedAt

		if err := counter.IncrTotalOrders(ctx, org, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}

		if err := counter.IncrTotalSales(ctx, org, pays, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}

		if ord.StoreId != "" {
			if err := counter.IncrStoreOrders(ctx, org, ord.StoreId, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}

			if err := counter.IncrStoreSales(ctx, org, ord.StoreId, pays, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}
		}
	},
)

var _ = New("load-counter-product-orders",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		ctx := db.Context
		ns, err := hashid.GetNamespace(db.Context, ord.Id())
		if err != nil {
			log.Warn("hash id decode error %v", err, ctx)
		}

		org := organization.New(db)
		org.Name = ns
		log.Debug("org name is %v", ns)

		t := ord.CreatedAt

		if err := counter.IncrTotalProductOrders(ctx, org, ord, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}

		if ord.StoreId != "" {
			if err := counter.IncrStoreProductOrders(ctx, org, ord.StoreId, ord, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}
		}
	},
)

var _ = New("load-counter-users",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "halcyon")

		return NoArgs
	},
	func(db *ds.Datastore, usr *user.User) {
		ctx := db.Context
		ns, err := hashid.GetNamespace(db.Context, usr.Id())
		if err != nil {
			log.Warn("hash id decode error %v", err, ctx)
		}

		org := organization.New(db)
		org.Name = ns
		log.Debug("org name is %v", ns)

		t := usr.CreatedAt

		if err := counter.IncrUsers(ctx, org, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
	},
)

var _ = New("load-counter-subscribers",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "halcyon")

		return NoArgs
	},
	func(db *ds.Datastore, sub *subscriber.Subscriber) {
		ctx := db.Context
		ns, err := hashid.GetNamespace(db.Context, sub.Id())
		if err != nil {
			log.Warn("hash id decode error %v", err, ctx)
		}

		org := organization.New(db)
		org.Name = ns
		log.Debug("org name is %v", ns)

		t := sub.CreatedAt

		if err := counter.IncrSubscribers(ctx, org, "94uloYDqfDKlbJ", t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
	},
)
