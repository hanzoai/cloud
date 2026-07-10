package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriptionitem"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createSubscriptionItemRequest struct {
	SubscriptionId string                 `json:"subscriptionId"`
	PriceId        string                 `json:"priceId,omitempty"`
	PlanId         string                 `json:"planId,omitempty"`
	MeterId        string                 `json:"meterId,omitempty"`
	Quantity       int64                  `json:"quantity,omitempty"`
	BillingMode    string                 `json:"billingMode,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// CreateSubscriptionItem adds an item to a subscription.
//
//	POST /v1/billing/subscription-items
func CreateSubscriptionItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createSubscriptionItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.SubscriptionId == "" {
		http.Fail(c, 400, "subscriptionId is required", nil)
		return
	}

	si := subscriptionitem.New(db)
	si.SubscriptionId = req.SubscriptionId
	si.PriceId = req.PriceId
	si.PlanId = req.PlanId
	si.MeterId = req.MeterId
	si.Quantity = req.Quantity

	if req.BillingMode != "" {
		si.BillingMode = req.BillingMode
	}
	if req.Metadata != nil {
		si.Metadata = req.Metadata
	}

	if err := si.Create(); err != nil {
		log.Error("Failed to create subscription item: %v", err, c)
		http.Fail(c, 500, "failed to create subscription item", err)
		return
	}

	c.JSON(201, subscriptionItemResponse(si))
}

// GetSubscriptionItem retrieves a subscription item by ID.
//
//	GET /v1/billing/subscription-items/:id
func GetSubscriptionItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := subscriptionitem.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "subscription item not found", err)
		return
	}

	c.JSON(200, subscriptionItemResponse(si))
}

// ListSubscriptionItems lists items for a subscription.
//
//	GET /v1/billing/subscription-items?subscriptionId=...
func ListSubscriptionItems(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	items := make([]*subscriptionitem.SubscriptionItem, 0)
	q := subscriptionitem.Query(db).Ancestor(rootKey)

	if subId := c.Query("subscriptionId"); subId != "" {
		q = q.Filter("SubscriptionId=", subId)
	}

	iter := q.Order("-Created").Run()
	for {
		si := subscriptionitem.New(db)
		if _, err := iter.Next(si); err != nil {
			break
		}
		items = append(items, si)
	}

	results := make([]map[string]interface{}, len(items))
	for i, si := range items {
		results[i] = subscriptionItemResponse(si)
	}
	c.JSON(200, results)
}

type updateSubscriptionItemRequest struct {
	Quantity int64                  `json:"quantity,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// UpdateSubscriptionItem updates a subscription item (e.g. seat count).
//
//	PATCH /v1/billing/subscription-items/:id
func UpdateSubscriptionItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := subscriptionitem.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "subscription item not found", err)
		return
	}

	var req updateSubscriptionItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Quantity > 0 {
		si.Quantity = req.Quantity
	}
	if req.Metadata != nil {
		si.Metadata = req.Metadata
	}

	if err := si.Update(); err != nil {
		log.Error("Failed to update subscription item: %v", err, c)
		http.Fail(c, 500, "failed to update subscription item", err)
		return
	}

	c.JSON(200, subscriptionItemResponse(si))
}

// DeleteSubscriptionItem removes an item from a subscription.
//
//	DELETE /v1/billing/subscription-items/:id
func DeleteSubscriptionItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := subscriptionitem.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "subscription item not found", err)
		return
	}

	if err := si.Delete(); err != nil {
		log.Error("Failed to delete subscription item: %v", err, c)
		http.Fail(c, 500, "failed to delete subscription item", err)
		return
	}

	c.JSON(200, gin.H{"deleted": true, "id": si.Id()})
}

func subscriptionItemResponse(si *subscriptionitem.SubscriptionItem) map[string]interface{} {
	resp := map[string]interface{}{
		"id":             si.Id(),
		"subscriptionId": si.SubscriptionId,
		"billingMode":    si.BillingMode,
		"quantity":       si.Quantity,
		"created":        si.Created,
	}
	if si.PriceId != "" {
		resp["priceId"] = si.PriceId
	}
	if si.PlanId != "" {
		resp["planId"] = si.PlanId
	}
	if si.MeterId != "" {
		resp["meterId"] = si.MeterId
	}
	if si.Metadata != nil {
		resp["metadata"] = si.Metadata
	}
	return resp
}
