package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	. "github.com/hanzoai/cloud/clients/commerce/models/fixtures"
)

var ImranForLuckyBets = New("imran-for-luckybets", func(c *gin.Context) {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "luckybets"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "dev@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Imran"
	u.LastName = "Hameed"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("NvK27bzuKmqBeBBH")
	u.MustUpdate()

	org.Owners = append(org.Owners, u.Id())
	org.MustUpdate()
})
