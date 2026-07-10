package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/setupintent"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createSetupIntentRequest struct {
	CustomerId      string `json:"customerId"`
	PaymentMethodId string `json:"paymentMethodId,omitempty"`
	Usage           string `json:"usage,omitempty"`
}

// CreateSetupIntent creates a new setup intent for saving a payment method.
//
//	POST /v1/billing/setup-intents
func CreateSetupIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createSetupIntentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	si, err := engine.CreateSetupIntent(db, engine.CreateSetupIntentParams{
		CustomerId:      req.CustomerId,
		PaymentMethodId: req.PaymentMethodId,
		Usage:           req.Usage,
	})
	if err != nil {
		log.Error("Failed to create setup intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(201, setupIntentResponse(si))
}

// GetSetupIntent retrieves a setup intent by ID.
//
//	GET /v1/billing/setup-intents/:id
func GetSetupIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := setupintent.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "setup intent not found", err)
		return
	}

	c.JSON(200, setupIntentResponse(si))
}

type confirmSetupIntentRequest struct {
	PaymentMethodId string `json:"paymentMethodId,omitempty"`
}

// ConfirmSetupIntent confirms a setup intent, saving the payment method.
//
//	POST /v1/billing/setup-intents/:id/confirm
func ConfirmSetupIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := setupintent.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "setup intent not found", err)
		return
	}

	var req confirmSetupIntentRequest
	_ = c.ShouldBindJSON(&req)

	if err := engine.ConfirmSetupIntent(c.Request.Context(), db, si, req.PaymentMethodId, nil); err != nil {
		log.Error("Failed to confirm setup intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(200, setupIntentResponse(si))
}

type cancelSetupIntentRequest struct {
	CancellationReason string `json:"cancellationReason,omitempty"`
}

// CancelSetupIntent cancels a setup intent.
//
//	POST /v1/billing/setup-intents/:id/cancel
func CancelSetupIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	si := setupintent.New(db)
	if err := si.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "setup intent not found", err)
		return
	}

	var req cancelSetupIntentRequest
	_ = c.ShouldBindJSON(&req)

	if err := engine.CancelSetupIntent(si, req.CancellationReason); err != nil {
		log.Error("Failed to cancel setup intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(200, setupIntentResponse(si))
}

func setupIntentResponse(si *setupintent.SetupIntent) map[string]interface{} {
	resp := map[string]interface{}{
		"id":         si.Id(),
		"customerId": si.CustomerId,
		"status":     si.Status,
		"usage":      si.Usage,
		"created":    si.Created,
	}
	if si.PaymentMethodId != "" {
		resp["paymentMethodId"] = si.PaymentMethodId
	}
	if si.ProviderRef != "" {
		resp["providerRef"] = si.ProviderRef
	}
	if si.LastError != "" {
		resp["lastError"] = si.LastError
	}
	if si.Metadata != nil {
		resp["metadata"] = si.Metadata
	}
	return resp
}
