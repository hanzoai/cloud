package billing

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type addLineItemRequest struct {
	Description string `json:"description"`
	Amount      int64  `json:"amount"`   // cents
	Quantity    int64  `json:"quantity"`
	MeterId     string `json:"meterId,omitempty"`
	PlanId      string `json:"planId,omitempty"`
	UnitPrice   int64  `json:"unitPrice,omitempty"`
}

// AddInvoiceLineItem appends a line item to a draft invoice and recalculates
// the subtotal.
//
//	POST /v1/billing/invoices/:id/line-items
func AddInvoiceLineItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	if inv.Status != billinginvoice.Draft {
		http.Fail(c, 400, "can only add line items to draft invoices", nil)
		return
	}

	var req addLineItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Description == "" {
		http.Fail(c, 400, "description is required", nil)
		return
	}

	// Compute amount from quantity * unitPrice if amount not provided
	amount := req.Amount
	if amount == 0 && req.Quantity > 0 && req.UnitPrice > 0 {
		amount = req.Quantity * req.UnitPrice
	}

	lineItem := billinginvoice.LineItem{
		Id:          fmt.Sprintf("li_%s_%d", id, len(inv.LineItems)+1),
		Type:        billinginvoice.LineOneOff,
		Description: req.Description,
		Amount:      amount,
		Quantity:    req.Quantity,
		UnitPrice:   req.UnitPrice,
		Currency:    inv.Currency,
	}

	if req.MeterId != "" {
		lineItem.Type = billinginvoice.LineUsage
		lineItem.MeterId = req.MeterId
	}
	if req.PlanId != "" {
		lineItem.Type = billinginvoice.LineSubscription
		lineItem.PlanId = req.PlanId
	}

	inv.LineItems = append(inv.LineItems, lineItem)
	inv.RecalculateSubtotal()

	// Recalculate AmountDue for draft: subtotal + tax - discount - credit
	inv.AmountDue = inv.Subtotal + inv.Tax - inv.Discount - inv.CreditApplied
	if inv.AmountDue < 0 {
		inv.AmountDue = 0
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to add line item: %v", err, c)
		http.Fail(c, 500, "failed to update invoice", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

// RemoveInvoiceLineItem removes a line item from a draft invoice by index
// or line item ID.
//
//	DELETE /v1/billing/invoices/:id/line-items/:itemId
func RemoveInvoiceLineItem(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	if inv.Status != billinginvoice.Draft {
		http.Fail(c, 400, "can only remove line items from draft invoices", nil)
		return
	}

	itemId := c.Param("itemId")
	if itemId == "" {
		http.Fail(c, 400, "itemId is required", nil)
		return
	}

	// Find and remove by line item ID
	found := false
	updated := make([]billinginvoice.LineItem, 0, len(inv.LineItems))
	for _, li := range inv.LineItems {
		if li.Id == itemId {
			found = true
			continue
		}
		updated = append(updated, li)
	}

	if !found {
		http.Fail(c, 404, "line item not found", nil)
		return
	}

	inv.LineItems = updated
	inv.RecalculateSubtotal()

	// Recalculate AmountDue
	inv.AmountDue = inv.Subtotal + inv.Tax - inv.Discount - inv.CreditApplied
	if inv.AmountDue < 0 {
		inv.AmountDue = 0
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to remove line item: %v", err, c)
		http.Fail(c, 500, "failed to update invoice", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

type applyDiscountRequest struct {
	CouponId       string `json:"couponId,omitempty"`
	DiscountAmount int64  `json:"discountAmount,omitempty"` // cents
	DiscountName   string `json:"discountName,omitempty"`
}

// ApplyInvoiceDiscount applies a discount to a draft invoice and recalculates
// the amount due.
//
//	POST /v1/billing/invoices/:id/apply-discount
func ApplyInvoiceDiscount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	if inv.Status != billinginvoice.Draft {
		http.Fail(c, 400, "can only apply discounts to draft invoices", nil)
		return
	}

	var req applyDiscountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.DiscountAmount <= 0 && req.CouponId == "" {
		http.Fail(c, 400, "discountAmount or couponId is required", nil)
		return
	}

	// If couponId is provided, look up the coupon amount.
	// For now, use the explicit discountAmount. Coupon lookup can be
	// added when a coupon model is wired in.
	discount := req.DiscountAmount
	name := req.DiscountName

	if req.CouponId != "" && name == "" {
		name = fmt.Sprintf("coupon:%s", req.CouponId)
	}

	// Cap discount at subtotal so we never go negative
	if discount > inv.Subtotal {
		discount = inv.Subtotal
	}

	inv.Discount = discount
	inv.DiscountName = name

	// Recalculate AmountDue
	inv.AmountDue = inv.Subtotal + inv.Tax - inv.Discount - inv.CreditApplied
	if inv.AmountDue < 0 {
		inv.AmountDue = 0
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to apply discount: %v", err, c)
		http.Fail(c, 500, "failed to update invoice", err)
		return
	}

	c.JSON(200, invoiceResponse(inv))
}

// CalculateInvoiceTax computes tax for an invoice based on a customer address
// and updates the invoice with the resulting tax lines.
//
//	POST /v1/billing/invoices/:id/calculate-tax?country=...&state=...
func CalculateInvoiceTax(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	// Build customer address from query parameters
	addr := &types.Address{
		Country:    c.Query("country"),
		State:      c.Query("state"),
		PostalCode: c.Query("postalCode"),
		City:       c.Query("city"),
	}

	if addr.Country == "" {
		http.Fail(c, 400, "country query parameter is required for tax calculation", nil)
		return
	}

	taxLines, totalTax, err := engine.CalculateInvoiceTax(db, inv, addr)
	if err != nil {
		log.Error("Failed to calculate tax: %v", err, c)
		http.Fail(c, 500, "failed to calculate tax", err)
		return
	}

	inv.Tax = totalTax
	if inv.Subtotal > 0 {
		inv.TaxPercent = float64(totalTax) / float64(inv.Subtotal) * 100
	}

	// Recalculate AmountDue
	inv.AmountDue = inv.Subtotal + inv.Tax - inv.Discount - inv.CreditApplied
	if inv.AmountDue < 0 {
		inv.AmountDue = 0
	}

	if err := inv.Update(); err != nil {
		log.Error("Failed to update invoice with tax: %v", err, c)
		http.Fail(c, 500, "failed to update invoice", err)
		return
	}

	c.JSON(200, gin.H{
		"invoice":  invoiceResponse(inv),
		"taxLines": taxLines,
		"totalTax": totalTax,
	})
}
