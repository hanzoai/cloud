package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

var _ = New("damon-forms", func(c *gin.Context) *form.Form {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "damon"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create mailinglist
	f := form.New(nsdb)
	f.Name = "Preorders"
	f.GetOrCreate("Name=", f.Name)
	f.Mailchimp.APIKey = ""
	f.MustUpdate()

	// Create mailinglist
	f2 := form.New(nsdb)
	f2.Name = "Newsletter"
	f2.GetOrCreate("Name=", f2.Name)
	f2.Mailchimp.APIKey = ""
	f2.Mailchimp.ListId = "aacc13e678"
	f2.MustUpdate()

	return f
})
