package shipwire

import (
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func (c *Client) CreateOrder(ord *order.Order, usr *user.User, opts OrderOptions) (*Order, *Response, error) {
	req := OrderRequest{}
	req.CommerceName = "Hanzo"
	req.OrderNo = strconv.Itoa(ord.Number)
	req.ExternalID = ord.Id()
	req.Options.ServiceLevelCode = opts.Service
	req.ShipTo.Name = ord.ShippingAddress.Name
	req.ShipTo.Email = usr.Email
	req.ShipTo.Address1 = ord.ShippingAddress.Line1
	req.ShipTo.Address2 = ord.ShippingAddress.Line2
	req.ShipTo.City = ord.ShippingAddress.City
	req.ShipTo.State = ord.ShippingAddress.State
	req.ShipTo.Country = ord.ShippingAddress.Country
	req.ShipTo.PostalCode = ord.ShippingAddress.PostalCode
	req.Items = make([]Item, len(ord.Items))

	for i, item := range ord.Items {
		req.Items[i] = Item{
			SKU:      item.SKU(),
			Quantity: item.Quantity,
		}
	}

	o := Order{}
	res, err := c.Resource("POST", "/orders", req, &o)
	if err != nil {
		return &o, res, err
	}

	ord.Fulfillment.Status = fulfillment.Pending
	ord.Fulfillment.Type = fulfillment.Shipwire
	ord.Fulfillment.ExternalId = strconv.Itoa(o.ID)
	ord.Fulfillment.CreatedAt = o.Events.Resource.CreatedDate.Time
	ord.Fulfillment.Service = o.Options.Resource.ServiceLevelCode
	ord.Fulfillment.Carrier = o.Options.Resource.CarrierCode
	ord.Fulfillment.SameDay = o.Options.Resource.SameDay

	return &o, res, ord.Update()
}

func (c *Client) GetOrder(id int) (*Order, *Response, error) {
	o := Order{}
	res, err := c.Resource("GET", "/orders/"+strconv.Itoa(id), nil, &o)
	return &o, res, err
}
