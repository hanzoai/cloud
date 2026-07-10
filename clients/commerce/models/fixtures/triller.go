package fixtures

import (
	// "time"
	"bytes"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("triller", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "triller"
	org.GetOrCreate("Name=", org.Name)

	usr := user.New(db)
	usr.Email = "triller@hanzo.ai"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "triller"
	usr.LastName = ""
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash("pp2triller!zO")
	usr.MustUpdate()

	org.FullName = "triller Inc"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://trillerfest.com/"}}
	org.EmailWhitelist = "*.hanzo.ai *.trillerfest.com"
	if bytes.Compare(org.SecretKey, []byte("rQVv9lIO0Yxy7msB")) != 0 {
		org.SecretKey = []byte("rQVv9lIO0Yxy7msB")
		org.AddDefaultTokens()
	}

	org.Fees.Card.Flat = 0
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 50
	org.Fees.Affiliate.Percent = 0.30

	// org.Mailchimp.APIKey = ""
	// org.Mailchimp.ListId = "7849878695"

	// Email configuration
	// org.Mandrill.APIKey = ""

	// org.Email.Enabled = true
	// org.Email.Defaults.From = email.Email{
	// 	Name:    "triller Motorcycles",
	// 	Address: "hi@trillerfest.com",
	// }

	// org.Email.Order.Confirmation.Subject = "triller Earphones Order Confirmation"
	// org.Email.Order.Confirmation.HTML = readEmailTemplate("/resources/triller/emails/order-confirmation.html")
	// org.Email.Order.Confirmation.Enabled = true

	// Save org into default namespace
	org.MustUpdate()

	// Save namespace so we can decode keys for this organization later
	// ns := namespace.New(db)
	// ns.Name = org.Name
	// ns.GetOrCreate("Name=", ns.Name)
	// ns.IntId = org.Key().IntID()
	// ns.MustUpdate()

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create default store
	stor := store.New(nsdb)
	stor.Name = "Website"
	stor.GetOrCreate("Name=", stor.Name)
	stor.Prefix = "/"
	stor.Currency = currency.USD
	// stor.Mailchimp.APIKey = ""
	// stor.Mailchimp.ListId = "7849878695"
	stor.MustUpdate()

	// Create motorcycle product
	// prod := product.New(nsdb)
	// prod.Slug = "HS"
	// prod.GetOrCreate("Slug=", prod.Slug)
	// prod.Name = "triller Motorcycles Hypersport HS Reservation"
	// prod.Description = ""
	// prod.Price = currency.Cents(10000)
	// prod.Inventory = 9000
	// prod.Preorder = true
	// prod.Hidden = false
	// prod.MustUpdate()

	return org
})
