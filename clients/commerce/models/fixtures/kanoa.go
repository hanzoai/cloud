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

var _ = New("kanoa", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "kanoa"
	org.GetOrCreate("Name=", org.Name)
	org.MustSetKey("8ATEOkEnSl")

	usr := user.New(db)
	usr.Email = "cival@getkanoa.com"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "Cival"
	usr.LastName = ""
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash("1Kanoa23")
	usr.MustUpdate()

	org.FullName = "KANOA Inc"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://getkanoa.com"}}
	org.SecretKey = []byte("EZ2E011iX2Bp5lv149N2STd1d580cU58")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	org.Mailchimp.APIKey = ""
	org.Mailchimp.ListId = "23ad4e4ba4"

	// Email configuration
	org.Mandrill.APIKey = ""

	org.Paypal.ConfirmUrl = "https://www.getkanoa.com"
	org.Paypal.CancelUrl = "https://www.getkanoa.com"

	org.Paypal.Live.Email = "cival@getkanoa.com"
	org.Paypal.Live.SecurityUserId = "cival_api1.getkanoa.com"
	org.Paypal.Live.ApplicationId = "APP-6PG93936C8597944N"
	org.Paypal.Live.SecurityPassword = "2YNUBS9TB9U7EDCM"
	org.Paypal.Live.SecuritySignature = "AFcWxV21C7fd0v3bYYYRCpSSRl31AZ6CAELso7zxPQz8gLc5YSsz6Iza"

	org.Paypal.Test.Email = "cival-facilitator@getkanoa.com"
	org.Paypal.Test.SecurityUserId = "cival-facilitator_api1.getkanoa.com"
	org.Paypal.Test.ApplicationId = "APP-80W284485P519543T"
	org.Paypal.Test.SecurityPassword = "XMDRP9CF75ESA8P8"
	org.Paypal.Test.SecuritySignature = "AcoBndPxINN2yEkgSKALAXYErpWTAFpUk3S6BucWeefHiUNpGxIleLof"

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "KANOA",
		Address: "hi@kanoa.com",
	}

	org.Email.Order.Confirmation.Subject = "KANOA Earphones Order Confirmation"
	org.Email.Order.Confirmation.HTML = readEmailTemplate("/resources/kanoa/emails/order-confirmation.html")
	org.Email.Order.Confirmation.Enabled = true

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
	stor.MustSetKey("7RtpEPYmCnJrnB")
	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = ""
	stor.Mailchimp.ListId = "23ad4e4ba4"
	stor.MustUpdate()

	// Create earphone product
	prod := product.New(nsdb)
	prod.Slug = "earphone"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.MustSetKey("84cguxepxk")
	prod.Name = "KANOA Earphone"
	prod.Description = "2 Ear Buds, 1 Charging Case, 3 Ergonomic Ear Tips, 1 Micro USB Cable"
	prod.Price = currency.Cents(19999)
	prod.Inventory = 9000
	prod.Preorder = true
	prod.Hidden = false
	prod.MustUpdate()

	return org
})
