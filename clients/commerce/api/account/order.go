package account

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func getOrder(c *gin.Context) {
	usr := middleware.GetUser(c)
	id := c.Params.ByName("orderid")

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ord := order.New(db)
	if err := ord.GetById(id); err != nil {
		http.Fail(c, 400, "Failed to query order", err)
		return
	}

	if usr.Id() != ord.UserId {
		http.Fail(c, 404, "Order does not exist", errors.New("Order does not exist"))
		return
	}

	http.Render(c, 200, ord)
}

func patchOrder(c *gin.Context) {
	usr := middleware.GetUser(c)
	id := c.Params.ByName("orderid")

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ord := order.New(db)
	if err := ord.GetById(id); err != nil {
		http.Fail(c, 400, "Failed to query order", err)
		return
	}

	if usr.Id() != ord.UserId {
		http.Fail(c, 404, "Order does not exist", errors.New("Order does not exist"))
		return
	}

	// We only want to extend the shipping address for right now
	// We use a second instance to decode into
	ord2 := order.New(db)

	// Set the address so we overlay
	ord2.ShippingAddress = ord.ShippingAddress

	// Decode into ord2
	if err := json.Decode(c.Request.Body, ord2); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	ord.ShippingAddress = ord2.ShippingAddress

	if err := ord.Put(); err != nil {
		http.Fail(c, 400, "Failed to update order", err)
	}

	http.Render(c, 200, ord)
}
