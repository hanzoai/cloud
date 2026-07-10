package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/refund"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createRefundRequest struct {
	PaymentIntentId string `json:"paymentIntentId,omitempty"`
	InvoiceId       string `json:"invoiceId,omitempty"`
	Amount          int64  `json:"amount,omitempty"` // 0 = full refund
	Reason          string `json:"reason,omitempty"`
}

// CreateRefund creates a full or partial refund.
//
//	POST /v1/billing/refunds
func CreateRefund(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	r, err := engine.CreateRefund(c.Request.Context(), db, engine.CreateRefundParams{
		PaymentIntentId: req.PaymentIntentId,
		InvoiceId:       req.InvoiceId,
		Amount:          req.Amount,
		Reason:          req.Reason,
	}, nil) // no external processor for now
	if err != nil {
		log.Error("Failed to create refund: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(201, refundResponse(r))
}

// GetRefund retrieves a refund by ID.
//
//	GET /v1/billing/refunds/:id
func GetRefund(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	r := refund.New(db)
	if err := r.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "refund not found", err)
		return
	}

	c.JSON(200, refundResponse(r))
}

// ListRefunds lists refunds, optionally filtered by paymentIntentId or invoiceId.
//
//	GET /v1/billing/refunds?paymentIntentId=...&invoiceId=...
func ListRefunds(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	refunds := make([]*refund.Refund, 0)
	q := refund.Query(db).Ancestor(rootKey)

	if piId := c.Query("paymentIntentId"); piId != "" {
		q = q.Filter("PaymentIntentId=", piId)
	}
	if invId := c.Query("invoiceId"); invId != "" {
		q = q.Filter("InvoiceId=", invId)
	}

	iter := q.Order("-Created").Run()
	for {
		r := refund.New(db)
		if _, err := iter.Next(r); err != nil {
			break
		}
		refunds = append(refunds, r)
	}

	results := make([]map[string]interface{}, len(refunds))
	for i, r := range refunds {
		results[i] = refundResponse(r)
	}
	c.JSON(200, results)
}

func refundResponse(r *refund.Refund) map[string]interface{} {
	resp := map[string]interface{}{
		"id":       r.Id(),
		"amount":   r.Amount,
		"currency": r.Currency,
		"status":   r.Status,
		"created":  r.Created,
	}
	if r.PaymentIntentId != "" {
		resp["paymentIntentId"] = r.PaymentIntentId
	}
	if r.InvoiceId != "" {
		resp["invoiceId"] = r.InvoiceId
	}
	if r.Reason != "" {
		resp["reason"] = r.Reason
	}
	if r.ProviderRef != "" {
		resp["providerRef"] = r.ProviderRef
	}
	if r.ReceiptNumber != "" {
		resp["receiptNumber"] = r.ReceiptNumber
	}
	if r.FailureReason != "" {
		resp["failureReason"] = r.FailureReason
	}
	if r.Metadata != nil {
		resp["metadata"] = r.Metadata
	}
	return resp
}
