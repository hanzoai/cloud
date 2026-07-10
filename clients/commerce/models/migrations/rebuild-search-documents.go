package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/demo/tokentransaction"
	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("rebuild-search-documents",
	func(c *gin.Context) []interface{} {
		db := ds.New(c)

		c.Set("namespace", "damon")
		db.SetNamespace("damon")

		// Search functionality removed
		// Document operations now handled by individual model PutDocument() calls

		return NoArgs
	},
	func(db *ds.Datastore, c *cart.Cart) {
		c.PutDocument()
	},
	func(db *ds.Datastore, u *user.User) {
		u.PutDocument()
	},
	func(db *ds.Datastore, t *tokentransaction.Transaction) {
		t.PutDocument()
	},
	func(db *ds.Datastore, o *order.Order) {
		o.PutDocument()
	},
	func(db *ds.Datastore, p *product.Product) {
		p.PutDocument()
	},
)
