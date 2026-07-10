package mandrill_integration_test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	log.SetVerbose(testing.Verbose())
	Setup("thirdparty/mandrill/integration", t)
}

var (
	ctx ae.Context
)

var _ = BeforeSuite(func() {
	if config.Mandrill.APIKey == "" {
		return
	}

	var err error
	ctx = ae.NewContext()
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if config.Mandrill.APIKey == "" {
		return
	}

	ctx.Close()
})

// var _ = Describe("Ping", func() {
// 	if config.Mandrill.APIKey == "" {
// 		return
// 	}

// 	It("Should return true from Ping", func() {
// 		Expect(mandrill.Ping(ctx)).To(Equal(true))
// 	})
// })

// var _ = Describe("Send", func() {
// 	if config.Mandrill.APIKey == "" {
// 		return
// 	}

// 	It("Should send email", func() {
// 		html := mandrill.GetTemplate("../../../../templates/email/order-confirmation.html")
// 		req := mandrill.NewSendReq()
// 		req.AddRecipient("dev@hanzo.ai", "Test Mandrill")

// 		req.Message.Subject = "Test subject"
// 		req.Message.FromEmail = "dev@hanzo.ai"
// 		req.Message.FromName = "Tester"
// 		req.Message.Html = html

// 		err := mandrill.Send(ctx, &req)
// 		Expect(err).NotTo(HaveOccurred())
// 	})
// })

// var _ = Describe("SendTemplate", func() {
// 	if config.Mandrill.APIKey == "" {
// 		return
// 	}

// 	It("Should send templated email", func() {
// 		req := mandrill.NewSendTemplateReq()
// 		// req.AddRecipient("dev@hanzo.ai", "Zach Kelling")
// 		// req.AddRecipient("dev@hanzo.ai", "Michael W")
// 		// req.AddRecipient("dev@hanzo.ai", "Marvel Mathew")
// 		// req.AddRecipient("dev@hanzo.ai", "David Tai")
// 		req.AddRecipient("dev@hanzo.ai", "Test Mandrill")

// 		req.Message.Subject = "Test subject"
// 		req.Message.FromEmail = "dev@hanzo.ai"
// 		req.Message.FromName = "Tester"
// 		req.TemplateName = "preorder-confirmation-template"

// 		err := mandrill.SendTemplate(ctx, &req)
// 		Expect(err).NotTo(HaveOccurred())
// 	})
// })
