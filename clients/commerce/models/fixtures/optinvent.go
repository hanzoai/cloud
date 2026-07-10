package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

var _ = New("optinvent", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "optinvent"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "optinvent@verus.io"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Michael"
	u.LastName = "Walker"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("optinventpassword!")
	u.Put()

	org.FullName = "Optinvent"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://www.optinvent.com"}}
	org.SecretKey = []byte("6OTgD1xuOAoqRYT4p8A0uf2g6ykNyQ5a")
	org.AddDefaultTokens()

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
