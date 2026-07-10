package provision

import (
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

// Take an organization and the owning user
func Provision(org *organization.Organization, usr *user.User) {
	// Make sure org exists
	if org.CreatedAt.IsZero() {
		org.MustCreate()
	}

	// Make sure user exists
	if usr.CreatedAt.IsZero() {
		usr.MustCreate()
	}

	// // Figure out ownership
	// if usr.Organizations == nil {
	// 	org.Owners
	// }

	// // Create default store
	// stor := store.New(nsdb)
	// stor.Name = "development"
	// stor.GetOrCreate("Name=", stor.Name)
	// stor.MustSetKey("KawdtZuoMY")
	// stor.Prefix = "/"
	// stor.Currency = currency.USD
	// stor.Mailchimp.APIKey = ""
	// stor.Mailchimp.ListId = "421751eb03"
	// stor.MustUpdate()

	// org.AddDefaultTokens()

	// org.MustUpdate()
}
