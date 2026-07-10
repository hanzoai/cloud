package fulfillment

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/fulfillmentmodel"
	"github.com/hanzoai/cloud/clients/commerce/models/fulfillmentprovider"
	"github.com/hanzoai/cloud/clients/commerce/models/fulfillmentset"
	"github.com/hanzoai/cloud/clients/commerce/models/geozone"
	"github.com/hanzoai/cloud/clients/commerce/models/servicezone"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingoption"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingoptionrule"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingprofile"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	rest.New(fulfillmentset.FulfillmentSet{}).Route(router, args...)
	rest.New(servicezone.ServiceZone{}).Route(router, args...)
	rest.New(geozone.GeoZone{}).Route(router, args...)
	rest.New(shippingoption.ShippingOption{}).Route(router, args...)
	rest.New(shippingoptionrule.ShippingOptionRule{}).Route(router, args...)
	rest.New(shippingprofile.ShippingProfile{}).Route(router, args...)
	rest.New(fulfillmentprovider.FulfillmentProvider{}).Route(router, args...)

	fApi := rest.New(fulfillmentmodel.Fulfillment{})
	fApi.POST("/:fulfillmentid/ship", namespaced, Ship)
	fApi.POST("/:fulfillmentid/cancel", namespaced, Cancel)
	fApi.Route(router, args...)
}

// ShipRequest holds the optional tracking labels sent when marking a
// fulfillment as shipped.
type ShipRequest struct {
	Labels []fulfillmentmodel.FulfillmentLabel `json:"labels"`
}

// Ship marks a fulfillment as shipped by setting ShippedAt to now and
// optionally appending tracking labels provided in the request body.
func Ship(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("fulfillmentid")

	f := fulfillmentmodel.New(db)
	if err := f.GetById(id); err != nil {
		http.Fail(c, 404, "No fulfillment found with id: "+id, err)
		return
	}

	if f.CanceledAt != nil {
		http.Fail(c, 400, "Fulfillment has been canceled", errors.New("fulfillment canceled"))
		return
	}

	// Parse optional labels from request body
	req := ShipRequest{}
	if c.Request.ContentLength > 0 {
		if err := json.Decode(c.Request.Body, &req); err != nil {
			http.Fail(c, 400, "Failed to decode request body", err)
			return
		}
	}

	now := time.Now()
	f.ShippedAt = &now

	if len(req.Labels) > 0 {
		f.Labels = append(f.Labels, req.Labels...)
	}

	if err := f.Update(); err != nil {
		http.Fail(c, 500, "Failed to update fulfillment", err)
		return
	}

	http.Render(c, 200, f)
}

// Cancel marks a fulfillment as canceled by setting CanceledAt to now.
func Cancel(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("fulfillmentid")

	f := fulfillmentmodel.New(db)
	if err := f.GetById(id); err != nil {
		http.Fail(c, 404, "No fulfillment found with id: "+id, err)
		return
	}

	if f.CanceledAt != nil {
		http.Fail(c, 400, "Fulfillment is already canceled", errors.New("already canceled"))
		return
	}

	now := time.Now()
	f.CanceledAt = &now

	if err := f.Update(); err != nil {
		http.Fail(c, 500, "Failed to cancel fulfillment", err)
		return
	}

	http.Render(c, 200, f)
}
