package test

import (
	"strings"

	"github.com/icrowley/fake"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("subscriber", func() {
	var normalize = func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}

	Context("New subscriber", func() {
		req := new(subscriber.Subscriber)
		res := new(subscriber.Subscriber)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req = subscriber.Fake(db, usr.Id())
			res = subscriber.New(db)

			// Create new subscriber
			cl.Post("/subscriber", req, res)
		})

		It("Should create new subscribers", func() {
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.FormId).To(Equal(req.FormId))
			Expect(res.Unsubscribed).To(Equal(req.Unsubscribed))
		})
	})

	Context("Get subscriber", func() {
		req := new(subscriber.Subscriber)
		res := new(subscriber.Subscriber)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req = subscriber.Fake(db, usr.Id())
			req.MustCreate()

			res = subscriber.New(db)

			cl.Get("/subscriber/"+req.Id(), res)
		})

		It("Should create new subscribers", func() {
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.UserId).To(Equal(req.UserId))
			Expect(res.FormId).To(Equal(req.FormId))
			Expect(res.Unsubscribed).To(Equal(req.Unsubscribed))
		})
	})

	Context("Patch subscriber", func() {
		sub := new(subscriber.Subscriber)
		res := new(subscriber.Subscriber)

		req := struct {
			Email string `json:"email"`
		}{
			fake.EmailAddress(),
		}

		Before(func() {
			// Create user
			usr := user.Fake(db)
			usr.MustCreate()

			// Create subscriber
			sub = subscriber.Fake(db, usr.Id())
			sub.MustCreate()

			// Patch subscriber
			cl.Patch("/subscriber/"+sub.Id(), req, res)
		})

		It("Should patch subscriber", func() {
			Expect(res.Id_).To(Equal(sub.Id()))
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.UserId).To(Equal(sub.UserId))
		})
	})

	Context("Put subscriber", func() {
		sub := new(subscriber.Subscriber)
		res := new(subscriber.Subscriber)
		req := new(subscriber.Subscriber)

		Before(func() {
			// Create user
			usr := user.Fake(db)
			usr.MustCreate()

			// Create subscriber
			sub = subscriber.Fake(db, usr.Id())
			sub.MustCreate()

			// Create subscriber request
			req = subscriber.Fake(db, usr.Id())

			// Update subscriber
			cl.Put("/subscriber/"+sub.Id(), req, res)
		})

		It("Should put subscriber", func() {
			Expect(res.Id_).To(Equal(sub.Id()))
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.UserId).To(Equal(req.UserId))
		})
	})

	Context("Delete subscriber", func() {
		res := ""

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req := subscriber.Fake(db, usr.Id())
			req.MustCreate()

			cl.Delete("/subscriber/" + req.Id())
			res = req.Id()
		})

		It("Should create new subscribers", func() {
			sub := subscriber.New(db)
			err := sub.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
