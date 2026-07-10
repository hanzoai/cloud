package mandrill_integration_test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/sendgrid"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	log.SetVerbose(testing.Verbose())
	Setup("thirdparty/sendgrid/integration", t)
}

var (
	ctx      ae.Context
	client   *sendgrid.API
	settings = integration.SendGrid{
		APIKey: config.SendGrid.APIKey,
	}
)

var _ = BeforeSuite(func() {
	var err error
	ctx = ae.NewContext()
	client = sendgrid.New(ctx, settings)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("Send", func() {
	if config.SendGrid.APIKey == "" {
		return
	}

	It("Should send email", func() {
		message := email.NewMessage()
		message.From = email.Email{
			Name:    "Hanzo",
			Address: "test@hanzo.ai",
		}
		message.AddTos(email.Email{
			Name:    "Hanzo Test",
			Address: "relay@hanzo.ai",
		})
		message.Subject = "Test"
		message.HTML = `
		<html>
		hi!
		</html>
		`
		message.Text = `
		Hi!
		`

		err := client.Send(message)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Should send email and apply substitutions", func() {
		message := email.NewMessage()
		message.From = email.Email{
			Name:    "Hanzo",
			Address: "test@hanzo.ai",
		}
		message.AddTos(email.Email{
			Name:    "Hanzo Test",
			Address: "relay@hanzo.ai",
		})
		message.Subject = "Test"
		message.HTML = `
		<html>
		Hi! -substitution-
		</html>
		`
		message.Text = `
		Hi! -substitution-
		`
		message.Substitutions["substitution"] = "Substitution works."

		err := client.Send(message)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Should send email and apply personalizations", func() {
		message := email.NewMessage()
		message.From = email.Email{
			Name:    "Hanzo",
			Address: "test@hanzo.ai",
		}
		message.AddTos(email.Email{
			Name:    "Hanzo Test",
			Address: "relay@hanzo.ai",
		})
		message.Subject = "Test"
		message.HTML = `
		<html>
		Hi -personalization-! -substitution-
		</html>
		`
		message.Text = `
		Hi -personalization-! -substitution-
		`
		message.Substitutions["substitution"] = "Substitution works."

		p := email.NewPersonalization()
		p.Substitutions["personalization"] = "Personalized Name"
		message.Personalizations["relay@hanzo.ai"] = p

		err := client.Send(message)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Should send email templates and apply personalizations and substitutions", func() {
		message := email.NewMessage()
		message.From = email.Email{
			Name:    "Hanzo",
			Address: "test@hanzo.ai",
		}
		message.AddTos(email.Email{
			Name:    "Hanzo Test",
			Address: "relay@hanzo.ai",
		})
		message.Subject = "Test"
		message.TemplateID = "1e5fce06-f67a-4c01-8dd6-d921090717c6"
		message.Substitutions["substitution"] = "Substitution works."

		p := email.NewPersonalization()
		p.Substitutions["personalization"] = "Personalized Name"
		message.Personalizations["relay@hanzo.ai"] = p

		err := client.Send(message)
		Expect(err).NotTo(HaveOccurred())
	})
})
