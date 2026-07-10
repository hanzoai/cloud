package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("referral", func() {
	Context("New referral", func() {
		req := new(referral.Referral)
		res := new(referral.Referral)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)
			ord.MustCreate()
			usr := user.Fake(db)
			usr.MustCreate()
			req = referral.Fake(db, usr.Id(), ord.Id())
			res = referral.New(db)

			// Create new referral
			cl.Post("/referral", req, res)
		})

		It("Should create new referrals", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.OrderId).To(Equal(req.OrderId))
			Expect(res.Referrer).To(Equal(req.Referrer))
			Expect(res.Fee).To(Equal(req.Fee))
		})
	})

	Context("Get referral", func() {
		req := new(referral.Referral)
		res := new(referral.Referral)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)
			ord.MustCreate()
			usr := user.Fake(db)
			usr.MustCreate()
			req = referral.Fake(db, usr.Id(), ord.Id())
			req.MustCreate()

			res = referral.New(db)

			cl.Get("/referral/"+req.Id(), res)
		})

		It("Should get referrals", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.OrderId).To(Equal(req.OrderId))
			Expect(res.Referrer).To(Equal(req.Referrer))
			Expect(res.Fee).To(Equal(req.Fee))
		})
	})

	Context("Patch referral", func() {
		rfl := new(referral.Referral)
		res := new(referral.Referral)

		req := struct {
			referral.Fee `json:"fee"`
		}{
			referral.Fee{
				Currency: currency.USD,
				Amount:   currency.Cents(0).FakeN(1000),
			},
		}

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)
			ord.MustCreate()
			usr := user.Fake(db)
			usr.MustCreate()
			rfl = referral.Fake(db, usr.Id(), ord.Id())
			rfl.MustCreate()

			res = referral.New(db)

			cl.Patch("/referral/"+rfl.Id(), req, res)
		})

		It("Should patch referral", func() {
			Expect(res.Id_).To(Equal(rfl.Id()))
			Expect(res.Type).To(Equal(rfl.Type))
			Expect(res.OrderId).To(Equal(rfl.OrderId))
			Expect(res.Referrer).To(Equal(rfl.Referrer))
			Expect(res.Fee).To(Equal(req.Fee))
		})
	})

	Context("Put referral", func() {
		rfl := new(referral.Referral)
		res := new(referral.Referral)
		req := new(referral.Referral)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)
			ord.MustCreate()
			usr := user.Fake(db)
			usr.MustCreate()
			rfl = referral.Fake(db, usr.Id(), ord.Id())
			rfl.MustCreate()

			req = referral.Fake(db, usr.Id(), ord.Id())
			res = referral.New(db)

			cl.Put("/referral/"+rfl.Id(), req, res)
		})

		It("Should put referral", func() {
			Expect(res.Id_).To(Equal(rfl.Id()))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.OrderId).To(Equal(req.OrderId))
			Expect(res.Referrer).To(Equal(req.Referrer))
			Expect(res.Fee).To(Equal(req.Fee))
		})
	})

	Context("Delete referral", func() {
		res := ""

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			ord := order.Fake(db, li)
			ord.MustCreate()
			usr := user.Fake(db)
			usr.MustCreate()
			req := referral.Fake(db, usr.Id(), ord.Id())
			req.MustCreate()

			// Create new referral
			cl.Delete("/referral/" + req.Id())
			res = req.Id()
		})

		It("Should delete referrals", func() {
			rfl := referral.New(db)
			err := rfl.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
