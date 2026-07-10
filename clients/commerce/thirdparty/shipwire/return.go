package shipwire

import (
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/models/order"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func (c *Client) CreateReturn(ord *order.Order, opts ReturnOptions) (*Return, *Response, error) {
	req := ReturnRequest{}

	// Save reference to our order
	req.ExternalID = ord.Id()

	// Set Shipwire original order id
	id, err := strconv.Atoi(ord.Fulfillment.ExternalId)
	if err != nil {
		return nil, nil, err
	}
	req.OriginalOrder.ID = id

	// Hardcode region for now
	if opts.WarehouseRegion != "" {
		req.Options.WarehouseRegion = opts.WarehouseRegion
	}

	// Configure return creation
	if opts.EmailCustomer {
		req.Options.EmailCustomer = 1
	}

	if opts.GeneratePrepaidLabel {
		req.Options.GeneratePrepaidLabel = 1
	}

	// Add items being returned
	req.Items = make([]Item, len(ord.Items))
	for i, item := range ord.Items {
		req.Items[i] = Item{
			SKU:      item.SKU(),
			Quantity: item.Quantity,
		}
	}

	r := Return{}
	res, err := c.Resource("POST", "/returns", req, &r)
	return &r, res, err
}

func (c *Client) CancelReturn(id string) (*Response, error) {
	return c.Resource("POST", "/returns/"+id+"/cancel", nil, nil)
}
