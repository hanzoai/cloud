package api

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func convertTracking(t Tracking) fulfillment.Tracking {
	trk := fulfillment.Tracking{}
	trk.Number = t.Tracking
	trk.ExternalId = strconv.Itoa(t.ID)
	trk.Url = t.Url
	trk.Carrier = t.Carrier
	trk.Summary = t.Summary
	trk.FirstScanRegion = t.FirstScanRegion
	trk.FirstScanPostalCode = t.FirstScanPostalCode
	trk.FirstScanCountry = t.FirstScanCountry
	trk.DeliveryCity = t.DeliveryCity
	trk.DeliveryRegion = t.DeliveryRegion
	trk.DeliveryPostalCode = t.DeliveryPostalCode
	trk.DeliveryCountry = t.DeliveryCountry

	trk.CreatedAt = t.TrackedDate.Time
	trk.DeliveredAt = t.DeliveredDate.Time
	trk.FirstScanAt = t.FirstScanDate.Time
	trk.LabelCreatedAt = t.LabelCreatedDate.Time
	trk.SummaryAt = t.SummaryDate.Time
	return trk
}

func getOrderFromShipwire(c *gin.Context, org *organization.Organization, ord *order.Order, id int) error {
	client := shipwire.New(c, org.Shipwire.Username, org.Shipwire.Password)
	o, res, err := client.GetOrder(id)
	if res.Status < 300 && err != nil {
		// Try using order number
		log.Info("Looking up order via order number: %s", o.OrderNo, c)
		return ord.GetById(o.OrderNo)
	}

	log.Warn("Failed to fetch Shipwire order", c)
	return fmt.Errorf("No matching order found for Shipwire order %d", id)
}

func getOrderForTracking(c *gin.Context, t Tracking) (*order.Order, error) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	ord := order.New(db)

	log.Info("Looking up order using OrderExternalID", c)
	id := t.OrderExternalID
	err := ord.GetById(id)
	if err != nil {
		log.Info("Fetching Shipwire order to lookup order by order number", c)
		err = getOrderFromShipwire(c, org, ord, t.OrderID)
	}

	return ord, nil
}

func updateOrderTracking(ord *order.Order, t Tracking) {
	// Check if we know about this tracking object already
	for i, trk := range ord.Fulfillment.Trackings {
		if trk.ExternalId == strconv.Itoa(t.ID) {
			log.Info("Found existing tracking object, updating")
			ord.Fulfillment.Trackings[i] = convertTracking(t)
			return
		}
	}

	// New tracking information, append
	log.Info("New tracking information")
	ord.Fulfillment.Trackings = append(ord.Fulfillment.Trackings, convertTracking(t))
}

func updateTracking(c *gin.Context, topic string, t Tracking) {
	log.Info("Fetching order associated with tracking %s", t.ID, c)
	ord, err := getOrderForTracking(c, t)
	if err != nil {
		log.Warn("Unable to find order for tracking '%s': %v", t.ID, err, c)
		c.String(200, "ok\n")
		return
	}

	log.Info("Found order: %s, updating tracking", ord.Id(), c)
	updateOrderTracking(ord, t)

	log.Info("Saving order", c)
	ord.MustPut()

	c.String(200, "ok\n")
}
