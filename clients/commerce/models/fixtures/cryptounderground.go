package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/models/webhook"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("cryptounderground", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "cryptounderground"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "david@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "David"
	u.LastName = "Tai"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("Xtr3Lk7R")
	u.Put()

	org.FullName = "Crypto Underground"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://www.cryptounderground.com"}}
	org.SecretKey = []byte("EGtFY6kqvTuMHsuSW6Qk5NduE22Xk39S")

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30
	org.Fees.Ethereum.Flat = 0 // 500000
	org.Fees.Ethereum.Percent = 0.05

	// Email configuration
	// org.Mandrill.APIKey = ""

	// org.Email.Defaults.Enabled = true
	// org.Email.Defaults.FromName = "Crypto Underground"
	// org.Email.Defaults.FromEmail = "hi@cryptounderground.com"

	// org.Email.OrderConfirmation.Subject = "KANOA Earphones Order Confirmation"
	// org.Email.OrderConfirmation.Template = readEmailTemplate("/resources/kanoa/emails/order-confirmation.html")
	// org.Email.OrderConfirmation.Enabled = true

	// org.Email.User.PasswordReset.Template = readEmailTemplate("/resources/kanoa/emails/user-password-reset.html")
	// org.Email.User.PasswordReset.Subject = "Reset your KANOA password"
	// org.Email.User.PasswordReset.Enabled = true

	// org.Email.User.EmailConfirmation.Template = readEmailTemplate("/resources/kanoa/emails/user-email-confirmation.html")
	// org.Email.User.EmailConfirmation.Subject = "Please confirm your email"
	// org.Email.User.EmailConfirmation.Enabled = true

	// org.Email.User.EmailConfirmed.Subject = "Thank you for confirming your email"
	// org.Email.User.EmailConfirmed.Template = readEmailTemplate("/resources/kanoa/emails/user-email-confirmed.html")
	// org.Email.User.EmailConfirmed.Enabled = false

	// Save org into default namespace
	org.MustUpdate()

	w := wallet.New(db)
	w.Id_ = "customer-wallet"
	w.UseStringKey = true
	w.GetOrCreate("Id_=", "customer-wallet")

	if a, _ := w.GetAccountByName("cryptounderground-test"); a == nil {
		if _, err := w.CreateAccount("cryptounderground-test", blockchains.EthereumRopstenType, []byte("7MdTrG3jzZD2h6T9src25r5aaC29MCyZ")); err != nil {
			panic(err)
		}
	}

	if a, _ := w.GetAccountByName("cryptounderground"); a == nil {
		if _, err := w.CreateAccount("cryptounderground", blockchains.EthereumType, []byte("7MdTrG3jzZD2h6T9src25r5aaC29MCyZ")); err != nil {
			panic(err)
		}
	}

	nsDb := datastore.New(org.Namespaced(c))

	wh := webhook.New(nsDb)
	wh.Name = "picatic-proxy"
	wh.GetOrCreate("Name=", "picatic-proxy")

	if wh.AccessToken == "" {
		wh.AccessToken = ""
		wh.Live = true
		wh.Url = "http://35.188.46.251/webhook"
		wh.Events = webhook.Events{
			"order.paid": true,
		}
		wh.Enabled = true
		wh.MustUpdate()
	}

	return org
})
