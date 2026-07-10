package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("amazingairfare", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "amazingairfare"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "mike@michaelgrabham.com"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Michael"
	u.LastName = "Grabham"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("amazingairfarepassword!")
	u.Put()

	org.FullName = "Amazing Airfare"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://amazingairfare.net"}}
	org.SecretKey = []byte("8z317zTRE005g4Am2kt19a1a56aS5969")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	// Email configuration
	// org.Mandrill.APIKey = ""

	// org.Paypal.ConfirmUrl = "https://www.getkanoa.com"
	// org.Paypal.CancelUrl = "https://www.getkanoa.com"

	// org.Paypal.Live.Email = "cival@getkanoa.com"
	// org.Paypal.Live.SecurityUserId = "cival_api1.getkanoa.com"
	// org.Paypal.Live.ApplicationId = "APP-6PG93936C8597944N"
	// org.Paypal.Live.SecurityPassword = "2YNUBS9TB9U7EDCM"
	// org.Paypal.Live.SecuritySignature = "AFcWxV21C7fd0v3bYYYRCpSSRl31AZ6CAELso7zxPQz8gLc5YSsz6Iza"

	// org.Paypal.Test.Email = "cival-facilitator@getkanoa.com"
	// org.Paypal.Test.SecurityUserId = "cival-facilitator_api1.getkanoa.com"
	// org.Paypal.Test.ApplicationId = "APP-80W284485P519543T"
	// org.Paypal.Test.SecurityPassword = "XMDRP9CF75ESA8P8"
	// org.Paypal.Test.SecuritySignature = "AcoBndPxINN2yEkgSKALAXYErpWTAFpUk3S6BucWeefHiUNpGxIleLof"

	// org.Email.Defaults.Enabled = true
	// org.Email.Defaults.FromName = "KANOA"
	// org.Email.Defaults.FromEmail = "hi@kanoa.com"

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
	org.Put()

	// Save namespace so we can decode keys for this organization later
	// ns := namespace.New(db)
	// ns.Name = org.Name
	// ns.IntId = org.Key().IntID()
	// err := ns.Put()
	// if err != nil {
	// 	log.Warn("Failed to put namespace: %v", err)
	// }

	return org
})
