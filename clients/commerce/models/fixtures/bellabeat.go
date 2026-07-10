package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

var _ = New("bellabeat", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "bellabeat"
	org.GetOrCreate("Name=", org.Name)

	// u := user.New(db)
	// u.Email = "bellabeat-shipstation@hanzo.ai"
	// u.GetOrCreate("Email=", u.Email)
	// u.FirstName = "Shipstation"
	// u.LastName = "API"
	// u.Organizations = []string{org.Id()}
	// u.PasswordHash, _ = password.Hash("xvMQrMv2c5dCFbVG")
	// u.Put()

	// u2 := user.New(db)
	// u2.Email = "marko@bellabeat.com"
	// u2.GetOrCreate("Email=", u2.Email)
	// u2.FirstName = "Marko"
	// u2.LastName = "Bozic"
	// u2.Organizations = []string{org.Id()}
	// u2.PasswordHash, _ = password.Hash("bellabeatpassword!")
	// u2.Put()

	// u3 := user.New(db)
	// u3.Email = "morena@bellabeat.com"
	// u3.GetOrCreate("Email=", u3.Email)
	// u3.FirstName = "Morena"
	// u3.LastName = "Šimatić"
	// u3.Organizations = []string{org.Id()}
	// u3.PasswordHash, _ = password.Hash("bellabeatpassword!")
	// u3.Put()

	// u4 := user.New(db)
	// u4.Email = "ivana@bellabeat.com"
	// u4.GetOrCreate("Email=", u4.Email)
	// u4.FirstName = "Ivana"
	// u4.LastName = "Skegro"
	// u4.Organizations = []string{org.Id()}
	// u4.PasswordHash, _ = password.Hash("bellabeatpassword!")
	// u4.Put()

	// org.FullName = "bellabeat"
	// org.Owners = []string{u.Id()}
	// org.Admins = []string{u2.Id()}
	// org.Website = "http://www.bellabeat.com"
	// org.SecretKey = []byte("yW83JZGLjkGJE2gMfB4i0bwEoP03yJa5")
	// // org.AddDefaultTokens()

	// // Email configuration
	// org.Mandrill.APIKey = ""

	// org.Email.Defaults.Enabled = true
	// org.Email.Defaults.FromName = "Bellabeat"
	// org.Email.Defaults.FromEmail = "hi@bellabeat.com"

	// org.Email.OrderConfirmation.Subject = "LEAF Order Confirmation"
	// org.Email.OrderConfirmation.Template = readEmailTemplate("/resources/bellabeat/emails/order-confirmation.html")
	// org.Email.OrderConfirmation.Enabled = true

	// org.Email.User.PasswordReset.Template = readEmailTemplate("/resources/bellabeat/emails/user-password-reset.html")
	// org.Email.User.PasswordReset.Subject = "Reset your Bellabeat password"
	// org.Email.User.PasswordReset.Enabled = true

	// org.Email.User.EmailConfirmation.Template = readEmailTemplate("/resources/bellabeat/emails/user-email-confirmation.html")
	// org.Email.User.EmailConfirmation.Subject = "Please confirm your email"
	// org.Email.User.EmailConfirmation.Enabled = true

	// org.Email.User.EmailConfirmed.Subject = "Thank you for confirming your email"
	// org.Email.User.EmailConfirmed.Template = readEmailTemplate("/resources/bellabeat/emails/user-email-confirmed.html")
	// org.Email.User.EmailConfirmed.Enabled = true

	// // Save org into default namespace
	// org.Put()

	// 	// Save namespace so we can decode keys for this organization later
	// 	ns := namespace.New(db)
	// 	ns.Name = org.Name
	// 	ns.IntId = org.Key().IntID()
	// 	err := ns.Put()
	// 	if err != nil {
	// 		log.Warn("Failed to put namespace: %v", err)
	// 	}

	return org
})
