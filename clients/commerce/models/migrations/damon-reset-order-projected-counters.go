package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

func MustNukeCounter4(db *ds.Datastore, tag string) {
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

var _ = New("damon-reset-order-projected-counters",
	func(c *gin.Context) []interface{} {
		orgName := "damon"

		c.Set("namespace", orgName)

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", orgName).Get(); err != nil {
			panic(err)
		}

		nsDb := ds.New(org.Namespaced(c))
		MustNukeCounter2(nsDb, "order.projected.revenue")
		MustNukeCounter2(nsDb, "order.projected.refunded.amount")

		// MustNukeCounter2(nsDb, "order.refunded.count")
		// MustNukeCounter2(nsDb, "order.refunded.amount")

		// prods := make([]*product.Product, 0)
		// if _, err := product.Query(nsDb).GetAll(&prods); err != nil {
		// 	panic(err)
		// }

		// for _, prod := range prods {
		// 	MustNukeCounter2(nsDb, "product."+prod.Id()+".projected.revenue")
		// 	MustNukeCounter2(nsDb, "product."+prod.Id()+".refunded.count")
		// 	MustNukeCounter2(nsDb, "product."+prod.Id()+".refunded.amount")
		// }

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		if ord.Test {
			return
		}

		if ord.PaymentStatus != payment.Paid && ord.PaymentStatus != payment.Refunded {
			return
		}

		// if ord.Status == "cancelled" {
		// 	return
		// }

		ctx := db.Context

		projectedPrice := 0
		// Calculate Projected
		for _, item := range ord.Items {
			log.Warn("item %v", item.ProjectedPrice, db.Context)
			prod := product.New(ord.Datastore())
			if err := prod.GetById(item.ProductId); err == nil {
				projectedPrice += item.Quantity * int(prod.ProjectedPrice)
			}
		}

		if err := counter.IncrementByAll(ctx, "order.projected.revenue", ord.StoreId, ord.ShippingAddress.Country, projectedPrice, ord.CreatedAt); err != nil {
			log.Error("order.projected.revenue error %v", err, db.Context)
		}

		if ord.Refunded != ord.Total {
			return
		}

		if err := counter.IncrementByAll(ctx, "order.projected.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, projectedPrice, ord.CreatedAt); err != nil {
			log.Error("order.projected.refunded.amount error %v", err, db.Context)
		}

	},
)
