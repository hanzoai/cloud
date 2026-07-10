package billing

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billingevent"
	"github.com/hanzoai/cloud/clients/commerce/models/webhookendpoint"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// ListBillingEvents lists billing events, optionally filtered by type or objectId.
//
//	GET /v1/billing/events?type=...&objectId=...
func ListBillingEvents(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	events := make([]*billingevent.BillingEvent, 0)
	q := billingevent.Query(db).Ancestor(rootKey)

	if eventType := c.Query("type"); eventType != "" {
		q = q.Filter("Type=", eventType)
	}
	if objectId := c.Query("objectId"); objectId != "" {
		q = q.Filter("ObjectId=", objectId)
	}

	iter := q.Order("-Created").Limit(100).Run()
	for {
		evt := billingevent.New(db)
		if _, err := iter.Next(evt); err != nil {
			break
		}
		events = append(events, evt)
	}

	results := make([]map[string]interface{}, len(events))
	for i, evt := range events {
		results[i] = billingEventResponse(evt)
	}
	c.JSON(200, results)
}

// GetBillingEvent retrieves a single billing event.
//
//	GET /v1/billing/events/:id
func GetBillingEvent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	evt := billingevent.New(db)
	if err := evt.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "billing event not found", err)
		return
	}

	c.JSON(200, billingEventResponse(evt))
}

type createWebhookEndpointRequest struct {
	Url         string   `json:"url"`
	Events      []string `json:"events,omitempty"`
	Description string   `json:"description,omitempty"`
}

// CreateWebhookEndpoint registers a new webhook endpoint.
//
//	POST /v1/billing/webhook-endpoints
func CreateWebhookEndpoint(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createWebhookEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Url == "" {
		http.Fail(c, 400, "url is required", nil)
		return
	}

	// Generate signing secret
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		http.Fail(c, 500, "failed to generate secret", err)
		return
	}
	secret := "whsec_" + hex.EncodeToString(secretBytes) // pragma: allowlist secret

	ep := webhookendpoint.New(db)
	ep.Url = req.Url
	ep.Secret = secret
	ep.Events = req.Events
	ep.Description = req.Description

	if err := ep.Create(); err != nil {
		log.Error("Failed to create webhook endpoint: %v", err, c)
		http.Fail(c, 500, "failed to create webhook endpoint", err)
		return
	}

	c.JSON(201, webhookEndpointResponse(ep, true))
}

// GetWebhookEndpoint retrieves a webhook endpoint.
//
//	GET /v1/billing/webhook-endpoints/:id
func GetWebhookEndpoint(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ep := webhookendpoint.New(db)
	if err := ep.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "webhook endpoint not found", err)
		return
	}

	c.JSON(200, webhookEndpointResponse(ep, false))
}

// ListWebhookEndpoints lists all webhook endpoints.
//
//	GET /v1/billing/webhook-endpoints
func ListWebhookEndpoints(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	endpoints := make([]*webhookendpoint.WebhookEndpoint, 0)
	iter := webhookendpoint.Query(db).Ancestor(rootKey).Order("-Created").Run()

	for {
		ep := webhookendpoint.New(db)
		if _, err := iter.Next(ep); err != nil {
			break
		}
		endpoints = append(endpoints, ep)
	}

	results := make([]map[string]interface{}, len(endpoints))
	for i, ep := range endpoints {
		results[i] = webhookEndpointResponse(ep, false)
	}
	c.JSON(200, results)
}

type updateWebhookEndpointRequest struct {
	Url         string   `json:"url,omitempty"`
	Events      []string `json:"events,omitempty"`
	Status      string   `json:"status,omitempty"`
	Description string   `json:"description,omitempty"`
}

// UpdateWebhookEndpoint updates a webhook endpoint configuration.
//
//	PATCH /v1/billing/webhook-endpoints/:id
func UpdateWebhookEndpoint(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ep := webhookendpoint.New(db)
	if err := ep.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "webhook endpoint not found", err)
		return
	}

	var req updateWebhookEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Url != "" {
		ep.Url = req.Url
	}
	if req.Events != nil {
		ep.Events = req.Events
	}
	if req.Status != "" {
		ep.Status = req.Status
	}
	if req.Description != "" {
		ep.Description = req.Description
	}

	if err := ep.Update(); err != nil {
		log.Error("Failed to update webhook endpoint: %v", err, c)
		http.Fail(c, 500, "failed to update webhook endpoint", err)
		return
	}

	c.JSON(200, webhookEndpointResponse(ep, false))
}

// DeleteWebhookEndpoint removes a webhook endpoint.
//
//	DELETE /v1/billing/webhook-endpoints/:id
func DeleteWebhookEndpoint(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ep := webhookendpoint.New(db)
	if err := ep.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "webhook endpoint not found", err)
		return
	}

	if err := ep.Delete(); err != nil {
		log.Error("Failed to delete webhook endpoint: %v", err, c)
		http.Fail(c, 500, "failed to delete webhook endpoint", err)
		return
	}

	c.JSON(200, gin.H{"deleted": true, "id": ep.Id()})
}

func billingEventResponse(evt *billingevent.BillingEvent) map[string]interface{} {
	resp := map[string]interface{}{
		"id":         evt.Id(),
		"type":       evt.Type,
		"objectType": evt.ObjectType,
		"objectId":   evt.ObjectId,
		"livemode":   evt.Livemode,
		"pending":    evt.Pending,
		"created":    evt.Created,
	}
	if evt.CustomerId != "" {
		resp["customerId"] = evt.CustomerId
	}
	if evt.Data != nil {
		resp["data"] = evt.Data
	}
	if evt.PreviousData != nil {
		resp["previousData"] = evt.PreviousData
	}
	return resp
}

func webhookEndpointResponse(ep *webhookendpoint.WebhookEndpoint, includeSecret bool) map[string]interface{} {
	resp := map[string]interface{}{
		"id":          ep.Id(),
		"url":         ep.Url,
		"status":      ep.Status,
		"events":      ep.Events,
		"description": ep.Description,
		"created":     ep.Created,
	}
	if includeSecret {
		resp["secret"] = ep.Secret // pragma: allowlist secret
	}
	if ep.Metadata != nil {
		resp["metadata"] = ep.Metadata
	}
	return resp
}
