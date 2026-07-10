package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var _ = New("kanoa-mailchimp", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "kanoa").Get()
	org.Mailchimp.APIKey = ""
	org.Mailchimp.ListId = "23ad4e4ba4"

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create new store
	stor := store.New(nsdb)
	stor.Name = "development"
	stor.GetOrCreate("Name=", stor.Name)
	// This is the production ID.
	// stor.MustSetKey("7RtpEPYmCnJrnB")

	// This is the development ID.
	stor.MustSetKey("MZbtooKHjM")

	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = ""
	stor.Mailchimp.ListId = "23ad4e4ba4"
	stor.MustUpdate()

	org.DefaultStore = stor.Id()
	org.MustUpdate()

	// Fetch earphones
	prod := product.New(nsdb)
	prod.Query().Filter("Slug=", "earphone").Get()
	prod.MustSetKey("84cguxepxk")
	prod.Image = Media{Type: MediaImage, Alt: "", Url: "https://d33wubrfki0l68.cloudfront.net/7cd9a799b858535a729417c142a9465e70255a79/80015/img/batch1coda.png", X: 1252, Y: 1115}

	// Create corresponding Mailchimp entities
	client := mailchimp.New(db.Context, org.Mailchimp)
	client.CreateStore(stor)
	client.CreateProduct(stor.Id(), prod)

	return org
})
