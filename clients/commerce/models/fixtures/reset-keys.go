package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

var _ = New("reset-keys", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "sec-demo"
	org.GetOrCreate("Name=", org.Name)

	org.AddDefaultTokens()
	org.MustUpdate()

	return org
})
