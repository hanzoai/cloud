package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
)

var _ = New("verus-forms", func(c *gin.Context) *form.Form {
	db := datastore.New(c)

	f := form.New(db)
	f.MustGetById("NEu14x75uv0Z6B")
	f.Forward.Name = "Sales"
	f.Forward.Email = "dev@hanzo.ai"
	f.Forward.Enabled = true

	return f
})
