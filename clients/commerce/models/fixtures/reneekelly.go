package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("reneekelly-alphie-treats", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	// Create organization
	org := organization.New(db)
	org.Name = "alphietreats"
	org.GetOrCreate("Name=", org.Name)
	org.Currency = currency.USD

	// Create admins
	u := user.New(db)
	u.Email = "info@alphietreats.com"
	u.GetOrCreate("Email=", u.Email)
	u.AddOrganization(org.Id())
	u.Put()

	// Configure org
	org.FullName = "Alphie Treats"
	org.AddOwner(u.Id())
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://alphietreats.com/"}}
	org.SecretKey = []byte("144B649A8E31D32BF2A73F4A652C3")
	org.AddDefaultTokens()

	// Email configuration
	// org.Mandrill.APIKey = config.Mandrill.APIKey

	// org.Email.Defaults.Enabled = true
	// org.Email.Defaults.FromName = "hanzo"
	// org.Email.Defaults.FromEmail = "hi@hanzo.com"

	// org.Email.OrderConfirmation.Subject = "LEAF Order Confirmation"
	// org.Email.OrderConfirmation.Template = readEmailTemplate("/resources/hanzo/emails/order-confirmation.html")
	// org.Email.OrderConfirmation.Enabled = true

	// org.Email.User.PasswordReset.Template = readEmailTemplate("/resources/hanzo/emails/user-password-reset.html")
	// org.Email.User.PasswordReset.Subject = "Reset your hanzo password"
	// org.Email.User.PasswordReset.Enabled = true

	// org.Email.User.EmailConfirmation.Template = readEmailTemplate("/resources/hanzo/emails/user-email-confirmation.html")
	// org.Email.User.EmailConfirmation.Subject = "Please confirm your email"
	// org.Email.User.EmailConfirmation.Enabled = true

	// org.Email.User.EmailConfirmed.Subject = "Thank you for confirming your email"
	// org.Email.User.EmailConfirmed.Template = readEmailTemplate("/resources/hanzo/emails/user-email-confirmed.html")
	// org.Email.User.EmailConfirmed.Enabled = true

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
