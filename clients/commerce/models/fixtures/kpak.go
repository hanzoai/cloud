package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("kpak", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "kpak"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "tark@mighty-studios.com"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Tark"
	u.LastName = "Abed"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("1Kpak23")
	u.Put()

	org.FullName = "K-Pak, Inc"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://www.kpakcase.com"}}
	org.SecretKey = []byte("EU8E022iX2Bp5lv931N2STd1d777cU58")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	// Email configuration
	org.Mandrill.APIKey = ""

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "K-pak",
		Address: "hi@kpakcase.com",
	}

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
	ns := namespace.New(db)
	ns.Name = org.Name
	ns.IntId = org.Key().IntID()
	err := ns.Put()
	if err != nil {
		log.Warn("Failed to put namespace: %v", err)
	}

	return org
})
