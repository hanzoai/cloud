package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/webhook"
	"github.com/icrowley/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("webhook", func() {
	Context("New webhook", func() {
		req := new(webhook.Webhook)
		res := new(webhook.Webhook)

		Before(func() {
			req = webhook.Fake(db)
			res = webhook.New(db)

			// Create new webhook
			cl.Post("/webhook", req, res)
		})

		It("Should create new webhooks", func() {
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Url).To(Equal(req.Url))
			Expect(res.Live).To(Equal(req.Live))
			Expect(res.All).To(Equal(req.All))
		})
	})

	Context("Get webhook", func() {
		req := new(webhook.Webhook)
		res := new(webhook.Webhook)

		Before(func() {
			req = webhook.Fake(db)
			req.MustCreate()

			res = webhook.New(db)

			cl.Get("/webhook/"+req.Id(), res)
		})

		It("Should get webhooks", func() {
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Url).To(Equal(req.Url))
			Expect(res.Live).To(Equal(req.Live))
			Expect(res.All).To(Equal(req.All))
		})
	})

	Context("Patch webhook", func() {
		wh := new(webhook.Webhook)
		res := new(webhook.Webhook)

		req := struct {
			Url string `json:"url"`
		}{
			fake.DomainName(),
		}

		Before(func() {
			// Save webhook
			wh = webhook.Fake(db)
			wh.MustCreate()

			// Patch webhook
			cl.Patch("/webhook/"+wh.Id(), req, res)
		})

		It("Should patch webhook", func() {
			Expect(res.Id_).To(Equal(wh.Id()))
			Expect(res.Enabled).To(Equal(wh.Enabled))
			Expect(res.Url).To(Equal(req.Url))
			Expect(res.Live).To(Equal(wh.Live))
			Expect(res.All).To(Equal(wh.All))
		})
	})

	Context("Put webhook", func() {
		wh := new(webhook.Webhook)
		res := new(webhook.Webhook)
		req := new(webhook.Webhook)

		Before(func() {
			// Save webhook
			wh = webhook.Fake(db)
			wh.MustCreate()

			req = webhook.Fake(db)

			// Put webhook
			cl.Put("/webhook/"+wh.Id(), req, res)
		})

		It("Should put webhook", func() {
			Expect(res.Id_).To(Equal(wh.Id()))
			Expect(res.Enabled).To(Equal(req.Enabled))
			Expect(res.Url).To(Equal(req.Url))
			Expect(res.Live).To(Equal(req.Live))
			Expect(res.All).To(Equal(req.All))
		})
	})

	Context("Delete webhook", func() {
		res := ""

		Before(func() {
			req := webhook.Fake(db)
			req.MustCreate()

			cl.Delete("/webhook/" + req.Id())
			res = req.Id()
		})

		It("Should delete webhooks", func() {
			hook := webhook.New(db)
			err := hook.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
