package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func updateFromTrackings(ord *order.Order, rsrc Resource) {
	if len(rsrc.Items) < 1 {
		return
	}

	trackings := make([]fulfillment.Tracking, len(rsrc.Items))
	for i, item := range rsrc.Items {
		var t Tracking
		if err := json.Unmarshal(item.Resource, &t); err == nil {
			trackings[i] = convertTracking(t)
		}
	}
	ord.Fulfillment.Trackings = trackings
}

func updateFromHolds(ord *order.Order, rsrc Resource) {
	if len(rsrc.Items) < 1 {
		return
	}

	holds := make([]fulfillment.Hold, len(rsrc.Items))
	for i, item := range rsrc.Items {
		var h Hold
		if err := json.Unmarshal(item.Resource, &h); err == nil {
			holds[i] = convertHold(h)
		}
	}
	ord.Fulfillment.Holds = holds
}

func updateOrder(c *gin.Context, topic string, o Order) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ord := order.New(db)
	id := o.ExternalID
	if id == "" {
		id = o.OrderNo
	}

	log.Info("Updating order '%s'", id, c)
	err := ord.GetById(id)
	if err != nil {
		log.Warn("Unable to find order '%s': %v", id, err, c)
		return
	}

	oldPricing := ord.Fulfillment.Pricing

	// Save Shipwire data
	ord.Fulfillment.Type = fulfillment.Shipwire
	ord.Fulfillment.ExternalId = strconv.Itoa(o.ID)

	// Update fulfillment states
	ord.Fulfillment.Status = fulfillment.Status(o.Status)
	ord.Fulfillment.Pricing = currency.Cents(o.Pricing.Resource.Total * 100)
	ord.Fulfillment.PricingEstimate = currency.Cents(o.PricingEstimate.Resource.Total * 100)
	ord.Fulfillment.SameDay = o.Options.Resource.SameDay
	ord.Fulfillment.Service = o.Options.Resource.ServiceLevelCode
	ord.Fulfillment.Carrier = o.Options.Resource.CarrierCode
	ord.Fulfillment.WarehouseId = strconv.Itoa(o.Options.Resource.WarehouseID)
	ord.Fulfillment.WarehouseRegion = o.Options.Resource.WarehouseRegion

	// Update dates
	ord.Fulfillment.CreatedAt = o.Events.Resource.CreatedDate.Time
	ord.Fulfillment.CancelledAt = o.Events.Resource.CancelledDate.Time
	ord.Fulfillment.CompletedAt = o.Events.Resource.CompletedDate.Time
	ord.Fulfillment.CreatedAt = o.Events.Resource.CreatedDate.Time
	ord.Fulfillment.ExpectedCompletedAt = o.Events.Resource.ExpectedCompletedDate.Time
	ord.Fulfillment.ExpectedAt = o.Events.Resource.ExpectedDate.Time
	ord.Fulfillment.ExpectedSubmittedAt = o.Events.Resource.ExpectedSubmittedDate.Time
	ord.Fulfillment.LastManualUpdateAt = o.Events.Resource.LastManualUpdateDate.Time
	ord.Fulfillment.PickedUpAt = o.Events.Resource.PickedUpDate.Time
	ord.Fulfillment.ProcessedAt = o.Events.Resource.ProcessedDate.Time
	ord.Fulfillment.ReturnedAt = o.Events.Resource.ReturnedDate.Time
	ord.Fulfillment.SubmittedAt = o.Events.Resource.SubmittedDate.Time

	updateFromTrackings(ord, o.Trackings.Resource)
	updateFromHolds(ord, o.Holds.Resource)

	ord.MustPut()

	if oldPricing != ord.Fulfillment.Pricing && ord.Fulfillment.Pricing != 0 {
		counter.IncrOrderShip(db.Context, ord, time.Now())
	}

	c.String(200, "ok\n")
}

func createOrder(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	// Decode return options
	opts := OrderOptions{}
	if err := json.Decode(c.Request.Body, &opts); err != nil {
		http.Fail(c, 400, fmt.Errorf("Failed to decode request body: %v", err), err)
		return
	}

	// Fetch order
	id := c.Params.ByName("orderid")
	ord := order.New(db)
	if err := ord.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Errorf("Unable to find order '%s'", id), err)
		return
	}

	// Fetch user
	usr := user.New(db)
	if err := usr.GetById(ord.UserId); err != nil {
		http.Fail(c, 404, fmt.Errorf("Unable to find user '%s'", ord.UserId), err)
		return
	}

	// Create order in Shipwire
	client := shipwire.New(c, org.Shipwire.Username, org.Shipwire.Password)
	_, res, err := client.CreateOrder(ord, usr, opts)

	if err != nil {
		http.Fail(c, res.Status, res.Message, err)
	}

	http.Render(c, 200, ord)
}
