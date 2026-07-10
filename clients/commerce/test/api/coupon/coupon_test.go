package test

import (
	"math/rand"
	"strings"
	"time"

	"github.com/icrowley/fake"

	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("coupon", func() {
	Context("New coupon", func() {
		req := new(coupon.Coupon)
		res := new(coupon.Coupon)

		Before(func() {
			req = coupon.Fake(db)
			res = coupon.New(db)

			// Create new coupon
			cl.Post("/coupon", req, res)
		})

		It("Should create new coupons", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Code_).To(Equal(strings.ToUpper(req.Code_)))
			Expect(res.Dynamic).To(Equal(req.Dynamic))
			Expect(res.StartDate).To(Equal(req.StartDate))
			Expect(res.EndDate).To(Equal(req.EndDate))
			Expect(res.Once).To(Equal(req.Once))
			Expect(res.Limit).To(Equal(req.Limit))
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Used).To(Equal(req.Used))
		})
	})

	Context("Get coupon", func() {
		req := new(coupon.Coupon)
		res := new(coupon.Coupon)

		Before(func() {
			// Create coupon
			req = coupon.Fake(db)
			req.MustCreate()

			// Make response for verification
			res = coupon.New(db)

			// Get coupon
			w := cl.Get("/coupon/"+req.Id(), res)
			log.Warn(w.Body.String())

		})

		It("Should get coupons", func() {
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Code_).To(Equal(strings.ToUpper(req.Code_)))
			Expect(res.Dynamic).To(Equal(req.Dynamic))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate))
			Expect(res.Once).To(Equal(req.Once))
			Expect(res.Limit).To(Equal(req.Limit))
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Used).To(Equal(req.Used))
		})
	})

	Context("Put coupon", func() {
		cpn := new(coupon.Coupon)
		res := new(coupon.Coupon)
		req := new(coupon.Coupon)

		Before(func() {
			cpn = coupon.Fake(db)
			cpn.MustCreate()

			// Create coupon request
			req = coupon.Fake(db)

			// Update coupon
			cl.Put("/coupon/"+cpn.Id(), req, res)
		})

		It("Should put coupon", func() {
			Expect(res.Id_).To(Equal(cpn.Id()))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Code_).To(Equal(strings.ToUpper(req.Code_)))
			Expect(res.Dynamic).To(Equal(req.Dynamic))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate))
			Expect(res.Once).To(Equal(req.Once))
			Expect(res.Limit).To(Equal(req.Limit))
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Used).To(Equal(req.Used))
		})
	})

	Context("patch coupon", func() {
		cpn := new(coupon.Coupon)
		res := new(coupon.Coupon)

		req := struct {
			Name      string    `json:"name"`
			StartDate time.Time `json:"startDate"`
			EndDate   time.Time `json:"endDate"`
		}{
			fake.FullName(),
			time.Date(rand.Intn(25)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC).UTC(),
			time.Date(rand.Intn(25)+2025, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC).UTC(),
		}

		Before(func() {
			cpn = coupon.Fake(db)
			cpn.MustCreate()

			// Update coupon
			cl.Patch("/coupon/"+cpn.Id(), req, res)
			log.JSON(req)
			log.JSON(res)
		})

		It("Should patch coupon", func() {
			Expect(res.Id_).To(Equal(cpn.Id()))
			Expect(res.Type).To(Equal(cpn.Type))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Code_).To(Equal(strings.ToUpper(cpn.Code_)))
			Expect(res.Dynamic).To(Equal(cpn.Dynamic))
			Expect(res.StartDate.UTC()).To(Equal(req.StartDate.UTC()))
			Expect(res.EndDate.UTC()).To(Equal(req.EndDate.UTC()))
			Expect(res.Once).To(Equal(cpn.Once))
			Expect(res.Limit).To(Equal(cpn.Limit))
			Expect(res.Enabled).To(Equal(cpn.Enabled))
			Expect(res.Amount).To(Equal(cpn.Amount))
			Expect(res.Used).To(Equal(cpn.Used))
		})
	})

	Context("Delete coupon", func() {
		var cpn *coupon.Coupon
		var id string

		Before(func() {
			// Create coupon
			cpn = coupon.Fake(db)
			cpn.MustCreate()

			// Delete it
			cl.Delete("/coupon/" + cpn.Id())

			id = cpn.Id()
		})

		It("Should delete coupons", func() {
			cpn2 := coupon.New(db)
			err := cpn2.GetById(id)
			Expect(err).ToNot(BeNil())
			Expect(cpn2.Code()).NotTo(Equal(cpn.Code()))
		})
	})
})
