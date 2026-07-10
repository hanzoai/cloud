package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var _ = New("kanoa-cs-users", func(c *gin.Context) {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "kanoa"
	org.GetOrCreate("Name=", org.Name)

	// u := user.New(db)
	// u.Email = "dev@hanzo.ai"
	// u.GetOrCreate("Email=", u.Email)
	// u.FirstName = "Lorenzo"
	// u.LastName = "Castillo"
	// u.Organizations = []string{org.Id()}
	// u.PasswordHash, _ = password.Hash("1Kanoa23")
	// u.Put()

	// u := user.New(db)
	// u.Email = "jordan@getkanoa.com"
	// u.GetOrCreate("Email=", u.Email)
	// u.FirstName = "Jordan"
	// u.LastName = "Shou"
	// u.Organizations = []string{org.Id()}
	// u.PasswordHash, _ = password.Hash("1Kanoa23")
	// u.Put()

	u := user.New(db)
	u.Email = "kyle@getkanoa.com"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Kyle"
	u.LastName = "Morrison"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("1Kanoa23")
	u.Put()
})
