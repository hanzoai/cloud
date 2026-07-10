package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/submission"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/icrowley/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("submission", func() {
	Context("New submission", func() {
		req := new(submission.Submission)
		res := new(submission.Submission)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req = submission.Fake(db, usr.Id())
			res = submission.New(db)

			// Create new submission
			cl.Post("/submission", req, res)
		})

		It("Should create new submissions", func() {
			Expect(res.Email).To(Equal(req.Email))
			Expect(res.UserId).To(Equal(req.UserId))
		})
	})

	Context("Get submission", func() {
		req := new(submission.Submission)
		res := new(submission.Submission)

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req = submission.Fake(db, usr.Id())
			req.MustCreate()

			res = submission.New(db)

			cl.Get("/submission/"+req.Id(), res)
		})

		It("Should get submissions", func() {
			Expect(res.Email).To(Equal(req.Email))
			Expect(res.UserId).To(Equal(req.UserId))
		})
	})

	Context("Patch submission", func() {
		sub := new(submission.Submission)
		res := new(submission.Submission)

		req := struct {
			Email string `json:"email"`
		}{
			fake.EmailAddress(),
		}

		Before(func() {
			// Create user
			usr := user.Fake(db)
			usr.MustCreate()

			// Create submission
			sub = submission.Fake(db, usr.Id())
			sub.MustCreate()

			// Patch submission
			cl.Patch("/submission/"+sub.Id(), req, res)
		})

		It("Should patch submission", func() {
			Expect(res.Id_).To(Equal(sub.Id()))
			Expect(res.Email).To(Equal(req.Email))
			Expect(res.UserId).To(Equal(sub.UserId))
		})
	})

	Context("Put submission", func() {
		sub := new(submission.Submission)
		res := new(submission.Submission)
		req := new(submission.Submission)

		Before(func() {
			// Create user
			usr := user.Fake(db)
			usr.MustCreate()

			// Create submission
			sub = submission.Fake(db, usr.Id())
			sub.MustCreate()

			// Create submission request
			req = submission.Fake(db, usr.Id())

			// Update submission
			cl.Put("/submission/"+sub.Id(), req, res)
		})

		It("Should put submission", func() {
			Expect(res.Id_).To(Equal(sub.Id()))
			Expect(res.Email).To(Equal(req.Email))
			Expect(res.UserId).To(Equal(req.UserId))
		})
	})

	Context("Delete submission", func() {
		res := ""

		Before(func() {
			usr := user.Fake(db)
			usr.MustCreate()
			req := submission.Fake(db, usr.Id())
			req.MustCreate()

			cl.Delete("/submission/" + req.Id())
			res = req.Id()

		})

		It("Should delete submissions", func() {
			sub := submission.New(db)
			err := sub.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
