package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
)

var _ = New("cycliq", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "cycliq"
	org.GetOrCreate("Name=", org.Name)

	// u := user.New(db)
	// u.Email = "andrew@cycliq.com"
	// u.GetOrCreate("Email=", u.Email)
	// u.FirstName = "Andrew"
	// u.LastName = "Hagen"
	// u.Organizations = []string{org.Id()}
	// u.PasswordHash, _ = password.Hash("cycliqpassword!")
	// u.Put()

	// u2 := user.New(db)
	// u2.Email = "ac@theblackeyeproject.co.uk"
	// u2.GetOrCreate("Email=", u2.Email)
	// u2.FirstName = "Andy"
	// u2.LastName = "Copely"
	// u2.Organizations = []string{org.Id()}
	// u2.PasswordHash, _ = password.Hash("cycliqpassword!")
	// u2.Put()

	// org.FullName = "Cycliq"
	// org.Owners = []string{u.Id(), u2.Id()}
	// org.Website = "http://cycliq.com"
	// org.SecretKey = []byte("3kfmczo801fdmur0QtOCRZptNfRNV0uNexi")
	// org.AddDefaultTokens()

	// Add default analytics config
	integrations := []Integration{
		Integration{
			Type: "facebook-pixel",
			Id:   "381279715352892",
		},
		Integration{
			Type:  "facebook-conversions",
			Id:    "6019083398147",
			Event: "Added Product",
		},
		Integration{
			Type:  "facebook-conversions",
			Id:    "6028661365947",
			Event: "Completed Order",
		},
		Integration{
			Type: "google-analytics",
			Id:   "UA-43175229-1",
		},
		Integration{
			Type:  "google-adwords",
			Id:    "984628795",
			Event: "Completed Order",
		},
	}
	org.Analytics = Analytics{Integrations: integrations}

	// Save org into default namespace
	org.MustPut()

	// // Save namespace so we can decode keys for this organization later
	// ns := namespace.New(db)
	// ns.Name = org.Name
	// ns.IntId = org.Key().IntID()
	// err := ns.Put()
	// if err != nil {
	// 	log.Warn("Failed to put namespace: %v", err)
	// }

	return org
})
