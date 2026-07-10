package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentintent"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createPaymentIntentRequest struct {
	CustomerId         string                 `json:"customerId"`
	Amount             int64                  `json:"amount"`
	Currency           string                 `json:"currency"`
	PaymentMethodId    string                 `json:"paymentMethodId,omitempty"`
	CaptureMethod      string                 `json:"captureMethod,omitempty"`
	ConfirmationMethod string                 `json:"confirmationMethod,omitempty"`
	SetupFutureUsage   string                 `json:"setupFutureUsage,omitempty"`
	Description        string                 `json:"description,omitempty"`
	ReceiptEmail       string                 `json:"receiptEmail,omitempty"`
	InvoiceId          string                 `json:"invoiceId,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

// CreatePaymentIntent creates a new payment intent.
//
//	POST /v1/billing/payment-intents
func CreatePaymentIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createPaymentIntentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	cur := currency.Type("usd")
	if req.Currency != "" {
		cur = currency.Type(req.Currency)
	}

	pi, err := engine.CreatePaymentIntent(db, engine.CreatePaymentIntentParams{
		CustomerId:         req.CustomerId,
		Amount:             req.Amount,
		Currency:           cur,
		PaymentMethodId:    req.PaymentMethodId,
		CaptureMethod:      req.CaptureMethod,
		ConfirmationMethod: req.ConfirmationMethod,
		SetupFutureUsage:   req.SetupFutureUsage,
		Description:        req.Description,
		ReceiptEmail:       req.ReceiptEmail,
		InvoiceId:          req.InvoiceId,
	})
	if err != nil {
		log.Error("Failed to create payment intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if req.Metadata != nil {
		pi.Metadata = req.Metadata
		_ = pi.Update()
	}

	c.JSON(201, paymentIntentResponse(pi))
}

// GetPaymentIntent retrieves a payment intent by ID.
//
//	GET /v1/billing/payment-intents/:id
func GetPaymentIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	pi := paymentintent.New(db)
	if err := pi.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payment intent not found", err)
		return
	}

	c.JSON(200, paymentIntentResponse(pi))
}

// ListPaymentIntents lists payment intents, optionally filtered by customerId.
//
//	GET /v1/billing/payment-intents?customerId=...
func ListPaymentIntents(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	intents := make([]*paymentintent.PaymentIntent, 0)
	q := paymentintent.Query(db).Ancestor(rootKey)

	if customerId := c.Query("customerId"); customerId != "" {
		q = q.Filter("CustomerId=", customerId)
	}

	iter := q.Order("-Created").Run()
	for {
		pi := paymentintent.New(db)
		if _, err := iter.Next(pi); err != nil {
			break
		}
		intents = append(intents, pi)
	}

	results := make([]map[string]interface{}, len(intents))
	for i, pi := range intents {
		results[i] = paymentIntentResponse(pi)
	}
	c.JSON(200, results)
}

type confirmPaymentIntentRequest struct {
	PaymentMethodId string `json:"paymentMethodId,omitempty"`
}

// ConfirmPaymentIntent confirms a payment intent.
//
//	POST /v1/billing/payment-intents/:id/confirm
func ConfirmPaymentIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	pi := paymentintent.New(db)
	if err := pi.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payment intent not found", err)
		return
	}

	var req confirmPaymentIntentRequest
	_ = c.ShouldBindJSON(&req)

	// No external processor for now — internal-only
	if err := engine.ConfirmPaymentIntent(c.Request.Context(), db, pi, req.PaymentMethodId, nil); err != nil {
		log.Error("Failed to confirm payment intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(200, paymentIntentResponse(pi))
}

type capturePaymentIntentRequest struct {
	AmountToCapture int64 `json:"amountToCapture,omitempty"`
}

// CapturePaymentIntent captures a previously authorized payment intent.
//
//	POST /v1/billing/payment-intents/:id/capture
func CapturePaymentIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	pi := paymentintent.New(db)
	if err := pi.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payment intent not found", err)
		return
	}

	var req capturePaymentIntentRequest
	_ = c.ShouldBindJSON(&req)

	if err := engine.CapturePaymentIntent(c.Request.Context(), db, pi, req.AmountToCapture, nil); err != nil {
		log.Error("Failed to capture payment intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(200, paymentIntentResponse(pi))
}

type cancelPaymentIntentRequest struct {
	CancellationReason string `json:"cancellationReason,omitempty"`
}

// CancelPaymentIntent cancels a payment intent.
//
//	POST /v1/billing/payment-intents/:id/cancel
func CancelPaymentIntent(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	pi := paymentintent.New(db)
	if err := pi.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payment intent not found", err)
		return
	}

	var req cancelPaymentIntentRequest
	_ = c.ShouldBindJSON(&req)

	if err := engine.CancelPaymentIntent(c.Request.Context(), pi, req.CancellationReason); err != nil {
		log.Error("Failed to cancel payment intent: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(200, paymentIntentResponse(pi))
}

func paymentIntentResponse(pi *paymentintent.PaymentIntent) map[string]interface{} {
	resp := map[string]interface{}{
		"id":                 pi.Id(),
		"customerId":         pi.CustomerId,
		"amount":             pi.Amount,
		"currency":           pi.Currency,
		"status":             pi.Status,
		"captureMethod":      pi.CaptureMethod,
		"confirmationMethod": pi.ConfirmationMethod,
		"amountCapturable":   pi.AmountCapturable,
		"amountReceived":     pi.AmountReceived,
		"created":            pi.Created,
	}
	if pi.PaymentMethodId != "" {
		resp["paymentMethodId"] = pi.PaymentMethodId
	}
	if pi.InvoiceId != "" {
		resp["invoiceId"] = pi.InvoiceId
	}
	if pi.Description != "" {
		resp["description"] = pi.Description
	}
	if pi.ProviderRef != "" {
		resp["providerRef"] = pi.ProviderRef
	}
	if pi.LastError != "" {
		resp["lastError"] = pi.LastError
	}
	if pi.Metadata != nil {
		resp["metadata"] = pi.Metadata
	}
	return resp
}
