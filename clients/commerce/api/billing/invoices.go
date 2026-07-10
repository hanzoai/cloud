package billing

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createInvoiceRequest struct {
	UserId         string                     `json:"userId"`
	CustomerEmail  string                     `json:"customerEmail"`
	SubscriptionId string                     `json:"subscriptionId"`
	Currency       string                     `json:"currency"`
	LineItems      []billinginvoice.LineItem  `json:"lineItems"`
	Metadata       map[string]interface{}     `json:"metadata"`
}

// CreateInvoice creates a new draft billing invoice.
//
//	POST /v1/billing/invoices
func CreateInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createInvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.UserId == "" {
		http.Fail(c, 400, "userId is required", nil)
		return
	}

	inv := billinginvoice.New(db)
	inv.UserId = req.UserId
	inv.CustomerEmail = req.CustomerEmail
	inv.SubscriptionId = req.SubscriptionId
	inv.LineItems = req.LineItems

	if req.Currency != "" {
		inv.Currency = currency.Type(strings.ToLower(req.Currency))
	}

	if req.Metadata != nil {
		inv.Metadata = req.Metadata
	}

	// Calculate subtotal from line items
	inv.RecalculateSubtotal()

	if err := inv.Create(); err != nil {
		log.Error("Failed to create invoice: %v", err, c)
		http.Fail(c, 500, "failed to create invoice", err)
		return
	}

	c.JSON(201, invoiceResponse(inv))
}

// ListInvoices lists billing invoices, optionally filtered by userId and status.
//
//	GET /v1/billing/invoices?userId=...&status=...
func ListInvoices(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	invoices := make([]*billinginvoice.BillingInvoice, 0)
	q := billinginvoice.Query(db).Ancestor(rootKey)

	userId := strings.TrimSpace(c.Query("userId"))
	if userId != "" {
		q = q.Filter("UserId=", userId)
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" {
		q = q.Filter("Status=", status)
	}

	subId := strings.TrimSpace(c.Query("subscriptionId"))
	if subId != "" {
		q = q.Filter("SubscriptionId=", subId)
	}

	if _, err := q.GetAll(&invoices); err != nil {
		log.Error("Failed to list invoices: %v", err, c)
		http.Fail(c, 500, "failed to list invoices", err)
		return
	}

	items := make([]gin.H, 0, len(invoices))
	for _, inv := range invoices {
		items = append(items, invoiceResponse(inv))
	}

	c.JSON(200, gin.H{
		"invoices": items,
		"count":    len(items),
	})
}

// GetInvoice returns a single billing invoice by ID.
//
//	GET /v1/billing/invoices/:id
func GetInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

// FinalizeInvoice transitions an invoice from draft to open.
//
//	POST /v1/billing/invoices/:id/finalize
func FinalizeInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	if err := inv.Finalize(); err != nil {
		http.Fail(c, 400, err.Error(), nil)
		return
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to finalize invoice: %v", err, c)
		http.Fail(c, 500, "failed to finalize invoice", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

// PayInvoice attempts to collect payment on an open invoice.
//
//	POST /v1/billing/invoices/:id/pay
func PayInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	result, err := engine.CollectInvoice(c.Request.Context(), db, inv, BurnCredits)
	if err != nil {
		log.Error("Failed to collect invoice: %v", err, c)
		http.Fail(c, 500, "failed to collect invoice payment", err)
		return
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to update invoice after payment: %v", err, c)
		http.Fail(c, 500, "failed to update invoice", err)
		return
	}

	c.JSON(200, gin.H{
		"invoice":   invoiceResponse(inv),
		"collection": result,
	})
}

// VoidInvoice voids a draft or open invoice.
//
//	POST /v1/billing/invoices/:id/void
func VoidInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	if err := inv.MarkVoid(); err != nil {
		http.Fail(c, 400, err.Error(), nil)
		return
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to void invoice: %v", err, c)
		http.Fail(c, 500, "failed to void invoice", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

// UpcomingInvoice generates a preview of the next invoice for a subscription.
//
//	GET /v1/billing/invoices/upcoming?userId=...&subscriptionId=...
func UpcomingInvoice(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	userId := strings.TrimSpace(c.Query("userId"))
	if userId == "" {
		http.Fail(c, 400, "userId query parameter is required", nil)
		return
	}

	// Aggregate current usage for this user
	lineItems, subtotal, err := engine.AggregateUsage(db, userId, time.Time{}, time.Time{})
	if err != nil {
		log.Error("Failed to aggregate usage: %v", err, c)
		http.Fail(c, 500, "failed to aggregate usage", err)
		return
	}

	// Apply credit burn-down for preview
	creditApplied := int64(0)
	amountDue := subtotal

	if subtotal > 0 {
		remaining, err := BurnCreditsPreview(db, userId, subtotal)
		if err == nil {
			creditApplied = subtotal - remaining
			amountDue = remaining
		}
	}

	c.JSON(200, gin.H{
		"userId":        userId,
		"lineItems":     lineItems,
		"subtotal":      subtotal,
		"creditApplied": creditApplied,
		"amountDue":     amountDue,
		"currency":      "usd",
	})
}

// BurnCreditsPreview calculates credit burn without actually deducting.
// Returns the remaining amount after credits would be applied.
func BurnCreditsPreview(db *datastore.Datastore, userId string, amount int64) (int64, error) {
	grants, err := getActiveGrants(db, userId)
	if err != nil {
		return amount, err
	}

	remaining := amount
	for _, g := range grants {
		if remaining <= 0 {
			break
		}
		deduct := g.RemainingCents
		if deduct > remaining {
			deduct = remaining
		}
		remaining -= deduct
	}

	return remaining, nil
}

func invoiceResponse(inv *billinginvoice.BillingInvoice) gin.H {
	resp := gin.H{
		"id":             inv.Id(),
		"userId":         inv.UserId,
		"customerEmail":  inv.CustomerEmail,
		"subscriptionId": inv.SubscriptionId,
		"periodStart":    inv.PeriodStart,
		"periodEnd":      inv.PeriodEnd,
		"subtotal":       inv.Subtotal,
		"tax":            inv.Tax,
		"discount":       inv.Discount,
		"creditApplied":  inv.CreditApplied,
		"amountDue":      inv.AmountDue,
		"amountPaid":     inv.AmountPaid,
		"currency":       inv.Currency,
		"status":         inv.Status,
		"paymentMethod":  inv.PaymentMethod,
		"paymentRef":     inv.PaymentRef,
		"number":         inv.Number,
		"numberStr":      inv.NumberStr,
		"attemptCount":   inv.AttemptCount,
		"lineItems":      inv.LineItems,
		"createdAt":      inv.CreatedAt,
		"updatedAt":      inv.UpdatedAt,
	}

	if !inv.DueDate.IsZero() {
		resp["dueDate"] = inv.DueDate
	}
	if !inv.PaidAt.IsZero() {
		resp["paidAt"] = inv.PaidAt
	}
	if !inv.VoidedAt.IsZero() {
		resp["voidedAt"] = inv.VoidedAt
	}

	return resp
}
