package test

import (
	"github.com/hanzoai/cloud/clients/commerce/api/checkout"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("shipstation", func() {
	Context("Export", func() {
		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)

			req := new(checkout.Authorization)
			req.Order = ord
			req.Payment = payment.Fake(db)
			req.User = user.Fake(db)

			// Create orders
			cl.Post("/checkout/charge", req, nil)
			cl.Post("/checkout/charge", req, nil)
			cl.Post("/checkout/charge", req, nil)
		})

		It("Should export orders", func() {
			w := bacl.Get("/shipstation/suchtees?action=export&start_date=01/02/2006 15:04&end_date=01/01/2020 16:20&page=1", nil)
			log.Error(w.Body)
		})
	})
})
