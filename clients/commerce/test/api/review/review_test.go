package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/review"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("review", func() {
	Context("New review", func() {
		req := new(review.Review)
		res := new(review.Review)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prod := product.Fake(db)
			prod.MustCreate()

			req = review.Fake(db, usr.Id(), prod.Id())
			res = review.New(db)

			// Create new referrer
			cl.Post("/review", req, res)
		})

		It("Should create new reviews", func() {
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.ProductId).To(Equal(req.ProductId))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Comment).To(Equal(req.Comment))
			Expect(res.Rating).To(Equal(req.Rating))
		})
	})

	Context("Get review", func() {
		req := new(review.Review)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			prod := product.Fake(db)
			prod.MustCreate()

			req = review.Fake(db, usr.Id(), prod.Id())
			req.Enabled = false
			req.MustCreate()
		})

		It("Should not get disabled reviews", func() {
			req.Enabled = false
			req.MustUpdate()
			cl.Get("/review/"+req.Id(), nil, 404)
		})

		It("Should get enabled reviews", func() {
			req.Enabled = true
			req.MustUpdate()
			cl.Get("/review/"+req.Id(), nil, 200)
		})
	})
})
