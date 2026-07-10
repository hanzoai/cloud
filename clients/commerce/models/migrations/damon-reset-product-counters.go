package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

func MustNukeCounter3(db *ds.Datastore, tag string) {
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

var _ = New("damon-reset-product-counters",
	func(c *gin.Context) []interface{} {
		orgName := "damon"

		c.Set("namespace", orgName)

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", orgName).Get(); err != nil {
			panic(err)
		}

		nsDb := ds.New(org.Namespaced(c))

		prods := make([]*product.Product, 0)
		if _, err := product.Query(nsDb).GetAll(&prods); err != nil {
			panic(err)
		}

		for _, prod := range prods {
			MustNukeCounter3(nsDb, "product."+prod.Id()+".sold")
			MustNukeCounter3(nsDb, "product."+prod.Id()+".revenue")
			MustNukeCounter3(nsDb, "product."+prod.Id()+".projected.revenue")

			MustNukeCounter3(nsDb, "product."+prod.Id()+".refunded.count")
			MustNukeCounter3(nsDb, "product."+prod.Id()+".refunded.amount")
			MustNukeCounter3(nsDb, "product."+prod.Id()+".projected.refunded.amount")
		}

		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		ctx := db.Context

		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", "damon").Get(); err != nil {
			log.Error("no org found %v", err, ctx)
		}

		usr := user.New(ord.Datastore())
		if err := usr.GetById(ord.UserId); err != nil {
			log.Error("no user found %v", err, ctx)
		}

		if ord.Test || org.IsTestEmail(usr.Email) {
			return
		}

		// Reject unpaid/fraud
		if ord.PaymentStatus != "paid" && ord.PaymentStatus != "refunded" {
			return
		}

		for _, item := range ord.Items {
			prod := product.New(ord.Datastore())
			if err := prod.GetById(item.ProductId); err != nil {
				log.Error("no product found %v", err, ctx)
			}
			for i := 0; i < item.Quantity; i++ {
				// Full Refunds
				if ord.Total == ord.Refunded {
					if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".refunded.count", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
						log.Error("product."+prod.Id()+".refunded.count error %v", err, ctx)
					}
					if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.Price), ord.CreatedAt); err != nil {
						log.Error("product."+prod.Id()+".refunded.amount error %v", err, ctx)
					}
					if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".projected.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
						log.Error("product."+prod.Id()+".projected.refunded.amount error %v", err, ctx)
					}
				}
				// Unrefunded or Partial Refunds
				if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".sold", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
					log.Error("product."+prod.Id()+".sold error %v", err, ctx)
					return
				}
				if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".revenue", ord.StoreId, ord.ShippingAddress.Country, int(prod.Price), ord.CreatedAt); err != nil {
					log.Error("product."+prod.Id()+".revenue error %v", err, ctx)
					return
				}
				if err := counter.IncrementByAll(ctx, "product."+prod.Id()+".projected.revenue", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
					log.Error("product."+prod.Id()+".projected.revenue error %v", err, ctx)
					return
				}
			}
		}
	},
)
