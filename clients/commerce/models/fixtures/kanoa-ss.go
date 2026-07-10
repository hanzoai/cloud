package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var _ = New("kanoa-ss", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "kanoa"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "shipstation@getkanoa.com"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "KANOA"
	u.LastName = "Shipstation"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("6bgX8LVwzwJaDwCd")
	u.Put()

	// Add to admins
	org.Admins = append(org.Admins, u.Id())

	// Save org into default namespace
	org.Put()

	return org
})
