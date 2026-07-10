package migrations

import (
	"github.com/gin-gonic/gin"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var _ = New("add-search-documents",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "cryptounderground")
		return NoArgs
	},
	func(db *ds.Datastore, u *user.User) {
		u.PutDocument()
	},
	func(db *ds.Datastore, o *order.Order) {
		o.PutDocument()
	},
	func(db *ds.Datastore, p *product.Product) {
		p.PutDocument()
	},
)
