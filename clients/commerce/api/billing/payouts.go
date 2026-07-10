package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/payout"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createPayoutRequest struct {
	Amount          int64                  `json:"amount"`
	Currency        string                 `json:"currency,omitempty"`
	DestinationType string                 `json:"destinationType"` // "bank_account" | "card"
	DestinationId   string                 `json:"destinationId"`
	Description     string                 `json:"description,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// CreatePayout creates a new outbound payout.
//
//	POST /v1/billing/payouts
func CreatePayout(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createPayoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Amount <= 0 {
		http.Fail(c, 400, "amount must be positive", nil)
		return
	}
	if req.DestinationId == "" {
		http.Fail(c, 400, "destinationId is required", nil)
		return
	}

	p := payout.New(db)
	p.Amount = req.Amount
	if req.Currency != "" {
		p.Currency = currency.Type(req.Currency)
	}
	p.DestinationType = req.DestinationType
	p.DestinationId = req.DestinationId
	p.Description = req.Description
	if req.Metadata != nil {
		p.Metadata = req.Metadata
	}

	if err := p.Create(); err != nil {
		log.Error("Failed to create payout: %v", err, c)
		http.Fail(c, 500, "failed to create payout", err)
		return
	}

	c.JSON(201, payoutResponse(p))
}

// GetPayout retrieves a payout by ID.
//
//	GET /v1/billing/payouts/:id
func GetPayout(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	p := payout.New(db)
	if err := p.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payout not found", err)
		return
	}

	c.JSON(200, payoutResponse(p))
}

// ListPayouts lists payouts.
//
//	GET /v1/billing/payouts
func ListPayouts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	payouts := make([]*payout.Payout, 0)
	iter := payout.Query(db).Ancestor(rootKey).Order("-Created").Run()

	for {
		p := payout.New(db)
		if _, err := iter.Next(p); err != nil {
			break
		}
		payouts = append(payouts, p)
	}

	results := make([]map[string]interface{}, len(payouts))
	for i, p := range payouts {
		results[i] = payoutResponse(p)
	}
	c.JSON(200, results)
}

// CancelPayout cancels a pending payout.
//
//	POST /v1/billing/payouts/:id/cancel
func CancelPayout(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	p := payout.New(db)
	if err := p.GetById(c.Param("id")); err != nil {
		http.Fail(c, 404, "payout not found", err)
		return
	}

	if err := p.Cancel(); err != nil {
		http.Fail(c, 400, err.Error(), err)
		return
	}

	if err := p.Update(); err != nil {
		log.Error("Failed to cancel payout: %v", err, c)
		http.Fail(c, 500, "failed to cancel payout", err)
		return
	}

	c.JSON(200, payoutResponse(p))
}

func payoutResponse(p *payout.Payout) map[string]interface{} {
	resp := map[string]interface{}{
		"id":              p.Id(),
		"amount":          p.Amount,
		"currency":        p.Currency,
		"status":          p.Status,
		"destinationType": p.DestinationType,
		"destinationId":   p.DestinationId,
		"created":         p.Created,
	}
	if p.Description != "" {
		resp["description"] = p.Description
	}
	if !p.ArrivalDate.IsZero() {
		resp["arrivalDate"] = p.ArrivalDate
	}
	if p.ProviderRef != "" {
		resp["providerRef"] = p.ProviderRef
	}
	if p.FailureCode != "" {
		resp["failureCode"] = p.FailureCode
		resp["failureMessage"] = p.FailureMessage
	}
	if p.Metadata != nil {
		resp["metadata"] = p.Metadata
	}
	return resp
}
