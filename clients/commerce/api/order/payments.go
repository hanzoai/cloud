package order

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func Payments(c *gin.Context) {
	id := c.Params.ByName("orderid")
	// Per-org store (Red MED-1): the order + its payments live in the caller
	// org's store. datastore.New(c) drops the namespace (gin.Context → Background)
	// and binds systemDB, so it 404s the order once the resolver is installed.
	org := middleware.GetOrganization(c)
	db := datastore.NewNamespaced(org.Namespaced(c))
	ord := order.New(db)

	err := ord.GetById(id)
	if err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve order %v: %v", id, err), err)
		return
	}

	payments := make([]*payment.Payment, 0)
	payment.Query(db).Ancestor(ord.Key()).GetAll(&payments)
	http.Render(c, 200, payments)
}
