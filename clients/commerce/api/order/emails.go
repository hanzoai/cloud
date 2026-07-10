package order

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/email"
)

func SendOrderConfirmation(c *gin.Context) {
	org := middleware.GetOrganization(c)
	// Per-org store (Red MED-1): the order is read via MustGetById, which PANICS
	// on a miss — datastore.New binds systemDB, so once the resolver is installed
	// the per-org order isn't found and the receipt handler 500s/panics.
	db := datastore.NewNamespaced(org.Namespaced(c))

	o := order.New(db)
	id := c.Params.ByName("orderid")
	o.MustGetById(id)

	u := user.New(db)
	u.MustGetById(o.UserId)

	email.SendOrderConfirmation(db.Context, org, o, u)

	c.Writer.WriteHeader(204)
}

func SendFulfillmentConfirmation(c *gin.Context) {
	org := middleware.GetOrganization(c)
	// Per-org store (Red MED-1): the order is read via MustGetById, which PANICS
	// on a miss — datastore.New binds systemDB, so once the resolver is installed
	// the per-org order isn't found and the receipt handler 500s/panics.
	db := datastore.NewNamespaced(org.Namespaced(c))

	o := order.New(db)
	id := c.Params.ByName("orderid")
	o.MustGetById(id)

	u := user.New(db)
	u.MustGetById(o.UserId)

	p := payment.New(db)
	p.MustGetById(o.PaymentIds[0])

	email.SendOrderShipped(db.Context, org, o, u, p)

	c.Writer.WriteHeader(204)
}

func SendRefundConfirmation(c *gin.Context) {
	org := middleware.GetOrganization(c)
	// Per-org store (Red MED-1): the order is read via MustGetById, which PANICS
	// on a miss — datastore.New binds systemDB, so once the resolver is installed
	// the per-org order isn't found and the receipt handler 500s/panics.
	db := datastore.NewNamespaced(org.Namespaced(c))

	o := order.New(db)
	id := c.Params.ByName("orderid")
	o.MustGetById(id)

	u := user.New(db)
	u.MustGetById(o.UserId)

	p := payment.New(db)
	p.MustGetById(o.PaymentIds[0])

	if o.Refunded == o.Paid {
		email.SendOrderRefunded(db.Context, org, o, u, p)
	} else if o.Refunded > 0 {
		email.SendOrderPartiallyRefunded(db.Context, org, o, u, p)
	}

	c.Writer.WriteHeader(204)
}

