package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/credit"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createCreditNoteRequest struct {
	InvoiceId       string                        `json:"invoiceId"`
	CustomerId      string                        `json:"customerId,omitempty"`
	Amount          int64                         `json:"amount,omitempty"`
	Reason          string                        `json:"reason,omitempty"`
	LineItems       []credit.CreditNoteLineItem `json:"lineItems,omitempty"`
	OutOfBandAmount int64                         `json:"outOfBandAmount,omitempty"`
	Memo            string                        `json:"memo,omitempty"`
}

// CreateCreditNote creates a credit note against an invoice.
//
//	POST /v1/billing/credit-notes
func CreateCreditNote(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createCreditNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.InvoiceId == "" {
		http.Fail(c, 400, "invoiceId is required", nil)
		return
	}

	cn, err := engine.CreateCreditNote(db, engine.CreateCreditNoteParams{
		InvoiceId:       req.InvoiceId,
		CustomerId:      req.CustomerId,
		Amount:          req.Amount,
		Reason:          req.Reason,
		LineItems:       req.LineItems,
		OutOfBandAmount: req.OutOfBandAmount,
		Memo:            req.Memo,
	})
	if err != nil {
		log.Error("Failed to create credit note: %v", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	c.JSON(201, creditNoteResponse(cn))
}

// GetCreditNote retrieves a credit note by ID.
//
//	GET /v1/billing/credit-notes/:id
func GetCreditNote(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	cn := credit.New(db)
	if err := cn.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "credit note not found", err)
		return
	}

	c.JSON(200, creditNoteResponse(cn))
}

// ListCreditNotes lists credit notes, optionally filtered by invoiceId or customerId.
//
//	GET /v1/billing/credit-notes?invoiceId=...&customerId=...
func ListCreditNotes(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	notes := make([]*credit.CreditNote, 0)
	q := credit.Query(db).Ancestor(rootKey)

	if invId := c.Query("invoiceId"); invId != "" {
		q = q.Filter("InvoiceId=", invId)
	}
	if custId := c.Query("customerId"); custId != "" {
		q = q.Filter("CustomerId=", custId)
	}

	iter := q.Order("-Created").Run()
	for {
		cn := credit.New(db)
		if _, err := iter.Next(cn); err != nil {
			break
		}
		notes = append(notes, cn)
	}

	results := make([]map[string]interface{}, len(notes))
	for i, cn := range notes {
		results[i] = creditNoteResponse(cn)
	}
	c.JSON(200, results)
}

// VoidCreditNote voids a credit note.
//
//	POST /v1/billing/credit-notes/:id/void
func VoidCreditNote(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	cn := credit.New(db)
	if err := cn.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "credit note not found", err)
		return
	}

	if err := cn.MarkVoid(); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if err := cn.Update(); err != nil {
		log.Error("Failed to void credit note: %v", err, c)
		http.Fail(c, 500, "failed to void credit note", err)
		return
	}

	c.JSON(200, creditNoteResponse(cn))
}

func creditNoteResponse(cn *credit.CreditNote) map[string]interface{} {
	resp := map[string]interface{}{
		"id":         cn.Id(),
		"invoiceId":  cn.InvoiceId,
		"customerId": cn.CustomerId,
		"number":     cn.Number,
		"amount":     cn.Amount,
		"currency":   cn.Currency,
		"status":     cn.Status,
		"created":    cn.Created,
	}
	if cn.Reason != "" {
		resp["reason"] = cn.Reason
	}
	if cn.LineItems != nil {
		resp["lineItems"] = cn.LineItems
	}
	if cn.OutOfBandAmount > 0 {
		resp["outOfBandAmount"] = cn.OutOfBandAmount
	}
	if cn.CreditBalanceTransaction != "" {
		resp["creditBalanceTransaction"] = cn.CreditBalanceTransaction
	}
	if cn.RefundId != "" {
		resp["refundId"] = cn.RefundId
	}
	if cn.Memo != "" {
		resp["memo"] = cn.Memo
	}
	if cn.Metadata != nil {
		resp["metadata"] = cn.Metadata
	}
	return resp
}
