package test

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/icrowley/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("referrer", func() {
	Context("New referrer", func() {
		req := new(referrer.Referrer)
		res := new(referrer.Referrer)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			req = referrer.Fake(db, usr.Id())
			res = referrer.New(db)

			// Create new referrer
			cl.Post("/referrer", req, res)
		})

		It("Should create new referrers", func() {
			Expect(res.Code).To(Equal(req.Code))
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.FirstReferredAt).To(Equal(req.FirstReferredAt))
		})
	})
	Context("Get referrer", func() {
		var aff *affiliate.Affiliate
		req := new(referrer.Referrer)
		res := new(referrer.Referrer)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			aff = affiliate.Fake(db, usr.Id())
			aff.MustCreate()

			req = referrer.Fake(db, usr.Id())
			req.AffiliateId = aff.Id()
			req.MustCreate()

			res = referrer.New(db)

			cl.Get("/referrer/"+req.Id(), res)
		})

		It("Should get referrers", func() {
			Expect(res.Code).To(Equal(req.Code))
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.Affiliate.Id_).To(Equal(aff.Id()))
			Expect(res.Affiliate.CouponId).To(Equal(aff.CouponId))
			Expect(res.FirstReferredAt.UTC()).To(Equal(req.FirstReferredAt.UTC()))
		})
	})

	Context("Patch referrer", func() {
		ref := new(referrer.Referrer)
		res := new(referrer.Referrer)

		req := struct {
			Code            string    `json:"Code"`
			FirstReferredAt time.Time `json:"firstReferredAt"`
		}{
			fake.Word(),
			time.Date(rand.Intn(15)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC),
		}

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			ref = referrer.Fake(db, usr.Id())
			ref.MustCreate()

			// Patch referrer
			cl.Patch("/referrer/"+ref.Id(), req, res)
		})

		It("Should patch referrer", func() {
			Expect(res.Id_).To(Equal(ref.Id()))
			Expect(res.Code).To(Equal(req.Code))
			Expect(res.UserId).To(Equal(ref.UserId))
			Expect(res.FirstReferredAt.UTC()).To(Equal(req.FirstReferredAt.UTC()))
		})
	})

	Context("Put referrer", func() {
		ref := new(referrer.Referrer)
		res := new(referrer.Referrer)
		req := new(referrer.Referrer)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			ref = referrer.Fake(db, usr.Id())
			ref.MustCreate()

			req = referrer.Fake(db, usr.Id())

			// Update referrer
			cl.Put("/referrer/"+ref.Id(), req, res)
		})

		It("Should put referrer", func() {
			Expect(res.Id_).To(Equal(ref.Id()))
			Expect(res.Code).To(Equal(req.Code))
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.FirstReferredAt.UTC()).To(Equal(req.FirstReferredAt.UTC()))
		})
	})

	Context("Delete referrer", func() {
		res := ""

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()

			req := referrer.Fake(db, usr.Id())
			req.MustCreate()

			cl.Delete("/referrer/" + req.Id())
			res = req.Id()
		})

		It("Should get referrers", func() {
			ref := referrer.New(db)
			err := ref.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
