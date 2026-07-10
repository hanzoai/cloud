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

var _ = New("sa-mailchimp", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "stoned").Get()
	org.Mailchimp.APIKey = ""
	org.Mailchimp.ListId = "421751eb03"

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create new store
	stor := store.New(nsdb)
	stor.Name = "prod"
	stor.GetOrCreate("Name=", stor.Name)
	// This is the production ID.
	// stor.MustSetKey("7RtpEPYmCnJrnB")

	// This is the development ID.
	// stor.MustSetKey("MZbtooKHjM")

	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = ""
	stor.Mailchimp.ListId = "421751eb03"
	stor.MustUpdate()

	org.DefaultStore = stor.Id()
	org.MustUpdate()

	// Fetch earphones
	prod := product.New(nsdb)
	prod.Query().Filter("Slug=", "earphone").Get()
	prod.MustSetKey("wycZ3j0kFP0JBv")
	prod.Image = Media{Type: MediaImage, Alt: "", Url: "https://gallery.mailchimp.com/0f2d8a2923efe4ed120afdd91/images/aa0dcac6-26ec-417a-82f7-34da109a2542.jpg", X: 643, Y: 336}

	// Create corresponding Mailchimp entities
	client := mailchimp.New(db.Context, org.Mailchimp)
	client.CreateStore(stor)
	client.CreateProduct(stor.Id(), prod)

	return org
})
