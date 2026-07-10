package inventory

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	inventoryModel "github.com/hanzoai/cloud/clients/commerce/models/inventory"
	"github.com/hanzoai/cloud/clients/commerce/models/inventorylevel"
	"github.com/hanzoai/cloud/clients/commerce/models/reservation"
	"github.com/hanzoai/cloud/clients/commerce/models/variantinventorylink"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	// Inventory Items - standard CRUD
	rest.New(inventoryModel.InventoryItem{}).Route(router, args...)

	// Inventory Levels with stock adjustment endpoint
	levelApi := rest.New(inventorylevel.InventoryLevel{})
	levelApi.POST("/:inventorylevelid/adjust", namespaced, AdjustStock)
	levelApi.Route(router, args...)

	// Reservations
	rest.New(reservation.ReservationItem{}).Route(router, args...)

	// Variant-Inventory Links
	rest.New(variantinventorylink.VariantInventoryLink{}).Route(router, args...)
}

// adjustRequest represents a stock adjustment request body.
type adjustRequest struct {
	StockedQuantity  *int `json:"stockedQuantity"`
	ReservedQuantity *int `json:"reservedQuantity"`
	IncomingQuantity *int `json:"incomingQuantity"`
}

// AdjustStock adjusts StockedQuantity, ReservedQuantity, and/or IncomingQuantity
// on an InventoryLevel by the delta values provided in the request body.
func AdjustStock(c *gin.Context) {
	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)
	id := c.Params.ByName("inventorylevelid")

	// Load existing inventory level
	level := inventorylevel.New(db)
	if err := level.GetById(id); err != nil {
		http.Fail(c, 404, "Inventory level not found", err)
		return
	}

	// Parse adjustment
	var adj adjustRequest
	if err := json.Decode(c.Request.Body, &adj); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	// Apply deltas
	if adj.StockedQuantity != nil {
		level.StockedQuantity += *adj.StockedQuantity
	}
	if adj.ReservedQuantity != nil {
		level.ReservedQuantity += *adj.ReservedQuantity
	}
	if adj.IncomingQuantity != nil {
		level.IncomingQuantity += *adj.IncomingQuantity
	}

	// Persist
	if err := level.Update(); err != nil {
		http.Fail(c, 500, "Failed to adjust inventory level", err)
		return
	}

	http.Render(c, 200, level)
}
