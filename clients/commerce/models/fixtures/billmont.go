package fixtures

import (
	// "time"
	"bytes"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("billmont", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "billmont"
	org.GetOrCreate("Name=", org.Name)

	usr := user.New(db)
	usr.Email = "billmont@hanzo.ai"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "billmont"
	usr.LastName = ""
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash("pp2billmont!zO")
	usr.MustUpdate()

	org.FullName = "Billmont"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://billmontmotorcycles.com/"}}
	org.EmailWhitelist = "*.hanzo.ai *.billmont.com"
	if bytes.Compare(org.SecretKey, []byte("XM8g9rRqQ4zKe7FsI8sDz7y0O3m6cwCu")) != 0 {
		org.SecretKey = []byte("XM8g9rRqQ4zKe7FsI8sDz7y0O3m6cwCu")
		org.AddDefaultTokens()
	}

	org.Fees.Card.Flat = 0
	org.Fees.Card.Percent = 0.01
	org.Fees.Affiliate.Flat = 50
	org.Fees.Affiliate.Percent = 0.30

	// org.Mailchimp.APIKey = ""
	// org.Mailchimp.ListId = "7849878695"

	// // Email configuration
	// org.Mandrill.APIKey = ""

	// org.Email.Enabled = true
	// org.Email.Defaults.From = email.Email{
	// 	Name:    "billmont Motorcycles",
	// 	Address: "hi@billmontmotorcycles.com",
	// }

	// org.Email.Order.Confirmation.Subject = "billmont Earphones Order Confirmation"
	// org.Email.Order.Confirmation.HTML = readEmailTemplate("/resources/billmont/emails/order-confirmation.html")
	// org.Email.Order.Confirmation.Enabled = true

	// Save org into default namespace
	org.MustUpdate()

	// // Save namespace so we can decode keys for this organization later
	// ns := namespace.New(db)
	// ns.Name = org.Name
	// ns.GetOrCreate("Name=", ns.Name)
	// ns.IntId = org.Key().IntID()
	// ns.MustUpdate()

	// nsdb := datastore.New(org.Namespaced(db.Context))

	return org
})
