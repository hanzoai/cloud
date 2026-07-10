package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/site"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("site", func() {
	Context("New site", func() {
		req := new(site.Site)
		res := new(site.Site)

		Before(func() {
			req = site.Fake(db)
			res = site.New(db)

			// Create new site
			log.JSON(req)
			// TODO: Our netlify reseller access is outdated right now, so this test won't work
			// cl.Post("/site", req, res)
			log.JSON(res)
		})

		It("Should create new sites", func() {
			Skip("netlify reseller access required/outdated right now")
			Expect(res.Domain).To(Equal(req.Domain))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Url).To(Equal(req.Url))
		})
	})
})
