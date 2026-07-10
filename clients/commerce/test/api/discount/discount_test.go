package test

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/icrowley/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("discount", func() {
	Context("New discount", func() {
		req := new(discount.Discount)
		res := new(discount.Discount)

		Before(func() {
			req = discount.Fake(db)
			res = discount.New(db)

			// Create new discount
			cl.Post("/discount", req, res)
		})

		It("Should create new discounts", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.StartDate).To(Equal(req.StartDate))
			Expect(res.EndDate).To(Equal(req.EndDate))
			Expect(res.Scope.Type).To(Equal(req.Scope.Type))
			Expect(res.Target.Type).To(Equal(req.Target.Type))
		})
	})
	Context("Get discount", func() {
		req := new(discount.Discount)
		res := new(discount.Discount)

		Before(func() {
			req = discount.Fake(db)
			req.MustCreate()

			res = discount.New(db)

			// Get discount
			cl.Get("/discount/"+req.Id(), res)
		})

		It("Should get discounts", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate.UTC()))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate.UTC()))
			Expect(res.Scope.Type).To(Equal(req.Scope.Type))
			Expect(res.Target.Type).To(Equal(req.Target.Type))
		})
	})

	Context("Patch discount", func() {
		dis := new(discount.Discount)
		res := new(discount.Discount)

		req := struct {
			Name      string    `json:"name"`
			StartDate time.Time `json:"startDate"`
			EndDate   time.Time `json:"endDate"`
		}{
			fake.Word(),
			time.Date(rand.Intn(25)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC).UTC(),
			time.Date(rand.Intn(25)+2025, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC).UTC(),
		}

		Before(func() {
			// Create discount
			dis = discount.Fake(db)
			dis.MustCreate()

			// Patch discount
			cl.Patch("/discount/"+dis.Id(), req, res)
		})

		It("Should patch discount", func() {
			Expect(res.Id_).To(Equal(dis.Id()))
			Expect(res.Type).To(Equal(dis.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate.UTC()))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate.UTC()))
			Expect(res.Scope.Type).To(Equal(dis.Scope.Type))
			Expect(res.Target.Type).To(Equal(dis.Target.Type))
		})
	})

	Context("Put discount", func() {
		dis := new(discount.Discount)
		res := new(discount.Discount)
		req := new(discount.Discount)

		Before(func() {
			// Create discount
			dis = discount.Fake(db)
			dis.MustCreate()

			// Create discount request
			req = discount.Fake(db)

			// Update discount
			cl.Put("/discount/"+dis.Id(), req, res)
		})

		It("Should put discount", func() {
			Expect(res.Id_).To(Equal(dis.Id()))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate.UTC()))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate.UTC()))
			Expect(res.Scope.Type).To(Equal(req.Scope.Type))
			Expect(res.Target.Type).To(Equal(req.Target.Type))
		})
	})

	Context("Delete discount", func() {
		res := ""

		Before(func() {
			req := discount.Fake(db)
			req.MustCreate()

			cl.Delete("/discount/" + req.Id())
			res = req.Id()
		})

		It("Should delete discounts", func() {
			d := discount.New(db)
			err := d.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
