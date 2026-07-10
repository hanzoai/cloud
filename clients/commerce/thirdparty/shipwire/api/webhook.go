package api

import (
	"net/http/httputil"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "encoding/json"
	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func getList(c *gin.Context, data []byte, dst interface{}) error {
	// Decode resource
	var rsrc Resource
	if err := json.Unmarshal(data, &rsrc); err != nil {
		log.Error("Failed decode resource: %v\n%s", err, data, c)
		return err
	}

	// Get individual items
	items := make([]RawMessage, len(rsrc.Items))
	for i := range rsrc.Items {
		items[i] = rsrc.Items[i].Resource
	}

	// Decode just items into slice dst
	if err := json.Unmarshal(json.EncodeBytes(items), dst); err != nil {
		log.Error("Failed decode: %v\n%s", err, items, c)
		return err
	}

	return nil
}

// Process individual webhooks
func webhook(c *gin.Context) {
	dump, _ := httputil.DumpRequest(c.Request, true)
	log.Info("Webhook request:\n%s", dump, c)

	var req Request
	if err := json.Decode(c.Request.Body, &req); err != nil {
		log.Error("Failed to decode request body: %v", err, c)
		c.String(200, "ok\n")
		return
	}

	switch req.Topic {
	case "order.created", "order.updated", "order.canceled", "order.completed":
		var o Order
		if err := json.Unmarshal(req.Body.Resource, &o); err != nil {
			log.Error("Failed decode resource: %v\n%s", err, req.Body.Resource, c)
		} else {
			updateOrder(c, req.Topic, o)
		}

	case "return.created", "return.updated", "return.canceled", "return.completed":
		var r Return
		if err := json.Unmarshal(req.Body.Resource, &r); err != nil {
			log.Error("Failed decode resource: %v\n%s", err, req.Body.Resource, c)
		} else {
			updateReturn(c, req.Topic, r)
		}

	case "tracking.created", "tracking.updated", "tracking.delivered":
		var t Tracking
		if err := json.Unmarshal(req.Body.Resource, &t); err != nil {
			log.Error("Failed decode resource: %v\n%s", err, req.Body.Resource, c)
		} else {
			updateTracking(c, req.Topic, t)
		}

	case "order.hold.added", "order.hold.cleared":
		holds := make([]Hold, 0)
		if err := getList(c, req.Body.Resource, &holds); err != nil {
			updateHolds(c, req.Topic, holds)
		}

	// case "return.hold.added", "return.hold.cleared":
	// 	holds := make([]Hold, 0)
	// 	if err := getList(c, req.Body.Resource, holds); err != nil {
	// 		updateReturnHolds(c, holds)
	// 	}

	// case "return.tracking.created", "return.tracking.updated", "return.tracking.delivered":
	// 	trackings := make([]Tracking, 0)
	// 	if err := getList(c, req.Body.Resource, trackings); err != nil {
	// 		updateReturnTrackings(c, trackings)
	// 	}

	default:
		c.String(200, "ok\n")
	}
}
