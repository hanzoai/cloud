package api

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func convertHold(h Hold) fulfillment.Hold {
	return fulfillment.Hold{
		Type:        h.Type + ":" + h.SubType,
		Description: h.Description,
		ExternalId:  h.ExternalOrderID,
		AppliedAt:   h.AppliedDate.Time,
	}
}

func updateHolds(c *gin.Context, topic string, holds []Hold) {
	log.Info("Holds:\n%v", holds, c)

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	// Handle no holds
	if len(holds) == 0 {
		c.String(200, "ok\n")
		return
	}

	// Grab first hold
	h := holds[0]

	ord := order.New(db)
	id := h.ExternalOrderID
	err := ord.GetById(id)
	if err != nil {
		log.Warn("Unable to find order '%s': %v", id, err, c)
		c.String(200, "ok\n")
		return
	}

	if err := ord.GetById(id); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	ord.Fulfillment.Status = fulfillment.Held
	ord.Fulfillment.Holds = make([]fulfillment.Hold, len(holds))
	for i := range holds {
		ord.Fulfillment.Holds[i] = convertHold(holds[i])
	}

	ord.MustPut()

	c.String(200, "ok\n")
}
