package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("crowdkeen", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "crowdkeen"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "dev@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "michael"
	u.LastName = "walker"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("1crowdkeen23")
	u.Update()

	org.FullName = "crowdkeen Inc"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://www.crowdkeen.net"}}
	org.SecretKey = []byte("EZ2E91RKX2BpRlv149N3STd1g580cp58")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	org.Mailchimp.APIKey = "4bf63eca9ca6594a7d37bf24aed84fcc-us14"
	org.Mailchimp.ListId = "36005"

	// Email configuration
	// org.Email.Defaults.Enabled = true
	// org.Email.Defaults.FromName = "crowdkeen"
	// org.Email.Defaults.FromEmail = "hi@crowdkeen.net"

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

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create default store
	stor := store.New(nsdb)
	stor.Name = "default"
	stor.GetOrCreate("Name=", stor.Name)
	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = "37181ee9311a3eb5999cf457a2216aca-us9"
	stor.Mailchimp.ListId = "27eb8e23aab"
	stor.Update()

	// Create earphone product
	prod := product.New(nsdb)
	prod.Slug = "earphone"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.Name = "KANOA Earphone"
	prod.Description = "2 Ear Buds, 1 Charging Case, 3 Ergonomic Ear Tips, 1 Micro USB Cable"
	prod.Price = currency.Cents(19999)
	prod.Inventory = 9000
	prod.Preorder = true
	prod.Hidden = false
	prod.Update()

	return org
})
