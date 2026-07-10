package api

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	return_ "github.com/hanzoai/cloud/clients/commerce/models/return"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
)

func createReturn(c *gin.Context) {
	id := c.Params.ByName("orderid")

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	// Decode return options
	opts := ReturnOptions{}
	if err := json.Decode(c.Request.Body, &opts); err != nil {
		http.Fail(c, 400, fmt.Errorf("Failed to decode request body: %v", err), err)
		return
	}

	// Fetch order
	ord := order.New(db)
	if err := ord.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Errorf("Unable to find order '%s'", id), err)
		return
	}

	// Create return in Shipwire
	client := shipwire.New(c, org.Shipwire.Username, org.Shipwire.Password)
	r, res, err := client.CreateReturn(ord, opts)

	if err != nil {
		http.Fail(c, res.Status, res.Message, err)
	}

	// Save return info
	rtn := return_.New(ord.Datastore())
	rtn.ExternalID = strconv.Itoa(r.ID)
	rtn.Summary = opts.Summary
	rtn.CancelledAt = r.Events.Resource.CancelledDate.Time
	rtn.CompletedAt = r.Events.Resource.CompletedDate.Time
	rtn.UpdatedAt = r.LastUpdatedDate.Time
	rtn.ExpectedAt = r.ExpectedDate.Time
	rtn.DeliveredAt = r.Events.Resource.DeliveredDate.Time
	rtn.PickedUpAt = r.Events.Resource.PickedUpDate.Time
	rtn.ProcessedAt = r.Events.Resource.ProcessedDate.Time
	rtn.ReturnedAt = r.Events.Resource.ReturnedDate.Time
	rtn.SubmittedAt = r.Events.Resource.SubmittedDate.Time
	rtn.OrderId = ord.Id()
	rtn.UserId = ord.UserId
	rtn.StoreId = ord.StoreId
	rtn.Status = r.Status

	for i, item := range rtn.Items {
		prod := product.New(db)
		if err := prod.GetById(item.ProductId); err != nil {
			http.Fail(c, 500, fmt.Errorf("Unable to find product '%s'", item.ProductId), err)
			return
		}
		rtn.Items[i].ExternalSKU = prod.SKU
	}

	if err := rtn.Create(); err != nil {
		http.Fail(c, 500, fmt.Errorf("Unable to save return '%s'", rtn.Id()), err)
	}

	items := rtn.Items
	if len(items) == 0 {
		items = ord.Items
	}

	if !ord.Test {
		if err := counter.IncrOrderReturn(db.Context, items, rtn); err != nil {
			log.Error("IncrOrderReturn Error %v", err, c)
		}
	}

	ord.ReturnIds = append(ord.ReturnIds, rtn.Id())

	if err := ord.Put(); err != nil {
		http.Fail(c, 500, fmt.Errorf("Unable to save return '%s'", rtn.Id()), err)
	}

	http.Render(c, 200, rtn)
}

func updateReturn(c *gin.Context, topic string, r Return) {
	log.Info("Update order information:\n%v", r, c)

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rtn := return_.New(db)
	id := r.ExternalID
	if err := rtn.GetById(id); err != nil {
		log.Warn("New return detected '%s'", c)
	}

	ord := order.New(db)
	if ok, err := ord.Query().Filter("Fulfillment.ExternalId=", r.ExternalID).Get(); err != nil {
		log.Warn("Unable to find order '%s': %v", r.ExternalID, err, c)
		c.String(200, "ok\n")
		return
	} else if !ok {
		log.Warn("Unable to find order '%s'", r.ExternalID, err, c)
		c.String(200, "ok\n")
		return
	}

	rtn.ExternalID = strconv.Itoa(r.ID)
	rtn.CancelledAt = r.Events.Resource.CancelledDate.Time
	rtn.CompletedAt = r.Events.Resource.CompletedDate.Time
	rtn.UpdatedAt = r.LastUpdatedDate.Time
	rtn.ExpectedAt = r.ExpectedDate.Time
	rtn.DeliveredAt = r.Events.Resource.DeliveredDate.Time
	rtn.PickedUpAt = r.Events.Resource.PickedUpDate.Time
	rtn.ProcessedAt = r.Events.Resource.ProcessedDate.Time
	rtn.ReturnedAt = r.Events.Resource.ReturnedDate.Time
	rtn.SubmittedAt = r.Events.Resource.SubmittedDate.Time
	rtn.OrderId = ord.Id()
	rtn.UserId = ord.UserId
	rtn.StoreId = ord.StoreId
	rtn.Status = r.Status

	// need to query something like
	// "items": {"resourceLocation": "http://api.shipwire.com/api/v3/returns/673/items?offset=0&limit=20&expand=all"},
	rtn.MustPut()

	c.String(200, "ok\n")
}
