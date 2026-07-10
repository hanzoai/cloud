package checkout

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/paypal"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

func cancel(c *gin.Context, org *organization.Organization, ord *order.Order) (err error) {
	// Handle payment cancellation
	switch ord.Type {
	case "paypal":
		err = paypal.Cancel(c, org, ord)
	default:
		return errors.New("Invalid order type")
	}

	return err
}
