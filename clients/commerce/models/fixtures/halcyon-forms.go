package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

var _ = New("halcyon-forms", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "halcyon"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create mailinglist
	f := form.New(nsdb)
	f.Name = "Mini-launch List"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4534419"
	f.MustUpdate()

	f = form.New(nsdb)
	f.Name = "Affiliates"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4780161"
	f.MustUpdate()

	f = form.New(nsdb)
	f.Name = "Ron White List"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4941545"
	f.MustUpdate()

	// Create earphone product
	return org
})
