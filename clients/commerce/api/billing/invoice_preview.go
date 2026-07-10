package billing

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/models/pricingrule"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type invoicePreviewRequest struct {
	UserId      string `json:"userId"`
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
}

type lineItem struct {
	MeterId   string `json:"meterId"`
	MeterName string `json:"meterName"`
	Quantity  int64  `json:"quantity"`
	UnitCost  int64  `json:"unitCost"`
	TotalCost int64  `json:"totalCost"`
	Model     string `json:"model"`
}

// InvoicePreview calculates an invoice preview: usage x pricing - credits.
//
//	POST /v1/billing/invoice-preview
func InvoicePreview(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req invoicePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.UserId == "" {
		http.Fail(c, 400, "userId is required", nil)
		return
	}

	var periodStart, periodEnd time.Time
	var err error

	if req.PeriodStart != "" {
		periodStart, err = parseDateOrTime(req.PeriodStart)
		if err != nil {
			http.Fail(c, 400, "invalid periodStart format", err)
			return
		}
	}

	if req.PeriodEnd != "" {
		periodEnd, err = parseDateOrTime(req.PeriodEnd)
		if err != nil {
			http.Fail(c, 400, "invalid periodEnd format", err)
			return
		}
	}

	// 1. Get all meters
	rootKey := db.NewKey("synckey", "", 1, nil)
	meters := make([]*meter.Meter, 0)
	mq := meter.Query(db).Ancestor(rootKey)
	if _, err := mq.GetAll(&meters); err != nil {
		log.Error("Failed to query meters: %v", err, c)
		http.Fail(c, 500, "failed to query meters", err)
		return
	}

	// 2. Get all pricing rules
	rules := make([]*pricingrule.PricingRule, 0)
	rq := pricingrule.Query(db).Ancestor(rootKey)
	if _, err := rq.GetAll(&rules); err != nil {
		log.Error("Failed to query pricing rules: %v", err, c)
		http.Fail(c, 500, "failed to query pricing rules", err)
		return
	}

	// Index pricing rules by meterId
	rulesByMeter := make(map[string]*pricingrule.PricingRule)
	for _, r := range rules {
		rulesByMeter[r.MeterId] = r
	}

	// 3. For each meter, aggregate events and calculate cost
	var totalCost int64
	lineItems := make([]lineItem, 0)

	for _, m := range meters {
		// Query events for this meter+user+period
		events := make([]*meter.MeterEvent, 0)
		eq := meter.QueryEvents(db).Ancestor(rootKey).
			Filter("MeterId=", m.Id()).
			Filter("UserId=", req.UserId)

		if !periodStart.IsZero() {
			eq = eq.Filter("Timestamp>=", periodStart)
		}
		if !periodEnd.IsZero() {
			eq = eq.Filter("Timestamp<=", periodEnd)
		}

		if _, err := eq.GetAll(&events); err != nil {
			log.Error("Failed to query meter events for %s: %v", m.Id(), err, c)
			continue
		}

		if len(events) == 0 {
			continue
		}

		// Aggregate
		var quantity int64
		for _, evt := range events {
			switch m.AggregationType {
			case meter.AggSum:
				quantity += evt.Value
			case meter.AggCount:
				quantity++
			case meter.AggLast:
				quantity = evt.Value
			default:
				quantity += evt.Value
			}
		}

		// Calculate cost using pricing rule
		rule, hasRule := rulesByMeter[m.Id()]
		var cost int64
		var pricingModel string

		if hasRule {
			cost = rule.CalculateCost(quantity)
			pricingModel = string(rule.PricingType)
		}

		if cost > 0 || quantity > 0 {
			var unitCost int64
			if hasRule {
				unitCost = rule.UnitPrice
			}
			lineItems = append(lineItems, lineItem{
				MeterId:   m.Id(),
				MeterName: m.Name,
				Quantity:  quantity,
				UnitCost:  unitCost,
				TotalCost: cost,
				Model:     pricingModel,
			})
			totalCost += cost
		}
	}

	// 4. Apply credit burn-down
	creditApplied := int64(0)
	overage := totalCost

	if totalCost > 0 {
		remaining, err := BurnCredits(db, req.UserId, totalCost, "")
		if err != nil {
			log.Error("Failed to calculate credit burn-down: %v", err, c)
			// Non-fatal: show preview without credits
		} else {
			creditApplied = totalCost - remaining
			overage = remaining
		}
	}

	c.JSON(200, gin.H{
		"userId":        req.UserId,
		"periodStart":   req.PeriodStart,
		"periodEnd":     req.PeriodEnd,
		"lineItems":     lineItems,
		"subtotal":      totalCost,
		"creditApplied": creditApplied,
		"amountDue":     overage,
		"currency":      "usd",
	})
}

func parseDateOrTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
