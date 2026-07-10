// Package engine provides core billing business logic: usage aggregation,
// payment collection, and subscription lifecycle management.
package engine

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/models/pricingrule"
)

// AggregateUsage queries all meters, collects events for the given user
// and period, applies pricing rules, and returns structured line items.
// This is the generalized version of the InvoicePreview handler logic.
func AggregateUsage(db *datastore.Datastore, userId string, periodStart, periodEnd time.Time) ([]billinginvoice.LineItem, int64, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)

	// 1. Get all meters
	meters := make([]*meter.Meter, 0)
	mq := meter.Query(db).Ancestor(rootKey)
	if _, err := mq.GetAll(&meters); err != nil {
		return nil, 0, err
	}

	// 2. Get all pricing rules, index by meterId
	rules := make([]*pricingrule.PricingRule, 0)
	rq := pricingrule.Query(db).Ancestor(rootKey)
	if _, err := rq.GetAll(&rules); err != nil {
		return nil, 0, err
	}

	rulesByMeter := make(map[string]*pricingrule.PricingRule)
	for _, r := range rules {
		rulesByMeter[r.MeterId] = r
	}

	// 3. For each meter, aggregate events and calculate cost
	var totalCost int64
	lineItems := make([]billinginvoice.LineItem, 0)

	for _, m := range meters {
		events := make([]*meter.MeterEvent, 0)
		eq := meter.QueryEvents(db).Ancestor(rootKey).
			Filter("MeterId=", m.Id()).
			Filter("UserId=", userId)

		if !periodStart.IsZero() {
			eq = eq.Filter("Timestamp>=", periodStart)
		}
		if !periodEnd.IsZero() {
			eq = eq.Filter("Timestamp<=", periodEnd)
		}

		if _, err := eq.GetAll(&events); err != nil {
			continue
		}

		if len(events) == 0 {
			continue
		}

		// Aggregate by meter type
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

		// Apply pricing rule
		rule, hasRule := rulesByMeter[m.Id()]
		var cost int64
		var unitPrice int64

		if hasRule {
			cost = rule.CalculateCost(quantity)
			unitPrice = rule.UnitPrice
		}

		if cost > 0 || quantity > 0 {
			lineItems = append(lineItems, billinginvoice.LineItem{
				Id:          "li_usage_" + m.Id(),
				Type:        billinginvoice.LineUsage,
				Description: m.Name + " usage",
				MeterId:     m.Id(),
				Quantity:    quantity,
				UnitPrice:   unitPrice,
				Amount:      cost,
				Currency:    m.Currency,
				PeriodStart: periodStart,
				PeriodEnd:   periodEnd,
			})
			totalCost += cost
		}
	}

	return lineItems, totalCost, nil
}
