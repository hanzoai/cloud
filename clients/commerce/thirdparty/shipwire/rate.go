package shipwire

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func (c *Client) Rate(ord *order.Order) ([]Rates, *RateResponse, error) {
	req := RateRequest{}

	req.Options.Currency = ord.Currency.Code()
	req.Options.CanSplit = 1
	req.Options.WarehouseArea = "US"
	req.Options.HighAccuracyEstimates = 1
	req.Options.ReturnAllRates = 1
	// req.Options.ChannelName = "My Channel"

	year, month, day := time.Now().Add(time.Hour * 24).Date()
	req.Options.ExpectedShipDate = fmt.Sprintf("%d-%d-%d", year, month, day)

	req.Order.ShipTo.Address1 = ord.ShippingAddress.Line1
	req.Order.ShipTo.Address2 = ord.ShippingAddress.Line2
	req.Order.ShipTo.City = ord.ShippingAddress.City
	req.Order.ShipTo.State = ord.ShippingAddress.State
	req.Order.ShipTo.Country = ord.ShippingAddress.Country
	req.Order.ShipTo.PostalCode = ord.ShippingAddress.PostalCode
	req.Order.Items = make([]Item, len(ord.Items))

	for i, item := range ord.Items {
		req.Order.Items[i] = Item{
			SKU:      item.SKU(),
			Quantity: item.Quantity,
		}
	}

	res := RateResponse{}

	// Use /api/v3.1/rate
	_, err := c.Request("POST", ".1/rate", req, &res)
	log.Info("Got res: %v", res, c.ctx)
	return res.Resource, &res, err
}
