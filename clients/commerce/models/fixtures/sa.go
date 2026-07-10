package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("stoned", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "stoned"
	org.GetOrCreate("Name=", org.Name)
	org.MustSetKey("4NTxXlQrtb")

	usr := user.New(db)
	usr.Email = "dev@hanzo.ai"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "Founders"
	usr.LastName = ""
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash("1Stoned23")
	usr.MustUpdate()

	org.FullName = "Stoned, LLC"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://www.stoned.audio"}}
	org.SecretKey = []byte("EK9E344442BI5nia9i82pdi98ip0jvqz")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	org.Mailchimp.APIKey = ""
	org.Mailchimp.ListId = "421751eb03"

	// Email configuration
	org.Mandrill.APIKey = ""

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Stoned Audio",
		Address: "dev@hanzo.ai",
	}

	//org.Email.OrderConfirmation.Subject = "Stoned Audio Order Confirmation"
	//org.Email.OrderConfirmation.Template = readEmailTemplate("/resources/sa/emails/order-confirmation.html")
	//org.Email.OrderConfirmation.Enabled = true

	//org.Email.User.PasswordReset.Template = readEmailTemplate("/resources/sa/emails/user-password-reset.html")
	//org.Email.User.PasswordReset.Subject = "Reset your password"
	//org.Email.User.PasswordReset.Enabled = true

	//org.Email.User.EmailConfirmation.Template = readEmailTemplate("/resources/sa/emails/user-email-confirmation.html")
	//org.Email.User.EmailConfirmation.Subject = "Please confirm your email"
	//org.Email.User.EmailConfirmation.Enabled = true

	//org.Email.User.EmailConfirmed.Subject = "Thank you for confirming your email"
	//org.Email.User.EmailConfirmed.Template = readEmailTemplate("/resources/sa/emails/user-email-confirmed.html")
	//org.Email.User.EmailConfirmed.Enabled = false

	// Legacy Stripe credentials removed

	// Save org into default namespace
	org.MustUpdate()

	// Save namespace so we can decode keys for this organization later
	ns := namespace.New(db)
	ns.Name = org.Name
	ns.GetOrCreate("Name=", ns.Name)
	ns.IntId = org.Key().IntID()
	ns.MustUpdate()

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create default store
	stor := store.New(nsdb)
	stor.Name = "development"
	stor.GetOrCreate("Name=", stor.Name)
	stor.MustSetKey("KawdtZuoMY")
	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = ""
	stor.Mailchimp.ListId = "421751eb03"
	stor.MustUpdate()

	// Create earphone product
	prod := product.New(nsdb)
	prod.Slug = "earphone"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.MustSetKey("MrbcmBZbsd")
	prod.Name = "Stoned Earphones"
	prod.Description = "2 Ear Buds, 1 Charging Case"
	prod.Price = currency.Cents(9999)
	prod.Inventory = 9000
	prod.Preorder = true
	prod.Hidden = false
	prod.MustUpdate()

	return org
})
