package fixtures

import (
	// "time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("platform", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "platform"
	org.GetOrCreate("Name=", org.Name)

	usr := user.New(db)
	usr.Email = "demon@hanzo.ai"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "Demon"
	usr.LastName = "Hanzo"
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash(":1platform2rulethemall:")
	usr.MustUpdate()

	org.FullName = "Hanzo Platform"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://hanzo.ai"}}
	org.EmailWhitelist = "*.hanzo.ai"
	org.AddDefaultTokens()

	// Email configuration
	org.Mailchimp.APIKey = ""
	org.Mandrill.APIKey = ""

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Demon Hanzo",
		Address: "demon@hanzo.ai",
	}

	// Save org into default namespace
	org.MustUpdate()

	// We create a namespace so we can decode keys for this organization later,
	// altho it should not be used for any "platform" data, as global platform
	// users are stored in the default namespace
	ns := namespace.New(db)
	ns.Name = org.Name
	ns.GetOrCreate("Name=", ns.Name)
	ns.IntId = org.Key().IntID()
	ns.MustUpdate()

	// Not needed for platform user
	// nsdb := datastore.New(org.Namespaced(db.Context))

	return org
})
