package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("store", func() {
	Context("New store", func() {
		req := new(store.Store)
		res := new(store.Store)

		Before(func() {
			req = store.Fake(db)
			res = store.New(db)

			cl.Post("/store", req, res)
		})

		It("Should create new stores", func() {
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Slug).To(Equal(req.Slug))
			Expect(res.Domain).To(Equal(req.Domain))
			Expect(res.Prefix).To(Equal(req.Prefix))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Address).To(Equal(req.Address))
			Expect(res.Email).To(Equal(req.Email))
		})
	})

	Context("Get store", func() {
		req := new(store.Store)
		res := new(store.Store)

		Before(func() {
			req = store.Fake(db)
			req.MustCreate()

			res = store.New(db)

			cl.Get("/store/"+req.Id(), res)
		})

		It("Should get stores", func() {
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Slug).To(Equal(req.Slug))
			Expect(res.Domain).To(Equal(req.Domain))
			Expect(res.Prefix).To(Equal(req.Prefix))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Address).To(Equal(req.Address))
			Expect(res.Email).To(Equal(req.Email))
		})
	})

	Context("Patch store", func() {
		stor := new(store.Store)
		res := new(store.Store)

		req := struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{
			fake.ProductName(),
			fake.Slug(),
		}

		Before(func() {
			// Create store
			stor = store.Fake(db)
			stor.MustCreate()

			// Patch store
			cl.Patch("/store/"+stor.Id(), req, res)
		})

		It("Should patch store", func() {
			Expect(res.Id_).To(Equal(stor.Id()))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Slug).To(Equal(req.Slug))
			Expect(res.Domain).To(Equal(stor.Domain))
			Expect(res.Prefix).To(Equal(stor.Prefix))
			Expect(res.Currency).To(Equal(stor.Currency))
			Expect(res.Address).To(Equal(stor.Address))
			Expect(res.Email).To(Equal(stor.Email))
		})
	})

	Context("Put store", func() {
		stor := new(store.Store)
		res := new(store.Store)
		req := new(store.Store)

		Before(func() {
			stor = store.Fake(db)
			stor.MustCreate()

			// Create store request
			req = store.Fake(db)

			// Update store
			cl.Put("/store/"+stor.Id(), req, res)
		})

		It("Should put store", func() {
			Expect(res.Id_).To(Equal(stor.Id()))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Slug).To(Equal(req.Slug))
			Expect(res.Domain).To(Equal(req.Domain))
			Expect(res.Prefix).To(Equal(req.Prefix))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Address).To(Equal(req.Address))
			Expect(res.Email).To(Equal(req.Email))
		})
	})

	Context("Delete store", func() {
		res := ""

		Before(func() {
			req := store.Fake(db)
			req.MustCreate()

			cl.Delete("/store/" + req.Id())
			res = req.Id()
		})

		It("Should delete stores", func() {
			stor := store.New(db)
			err := stor.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
