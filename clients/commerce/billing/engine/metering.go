package engine

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/models/pricingrule"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriptionitem"
	"github.com/hanzoai/cloud/clients/commerce/models/usagewatermark"
)

// UsageSummary describes aggregated usage for a meter+user over a period.
type UsageSummary struct {
	MeterId         string    `json:"meterId"`
	MeterName       string    `json:"meterName"`
	AggregationType string    `json:"aggregationType"`
	Value           int64     `json:"value"`
	EventCount      int64     `json:"eventCount"`
	PeriodStart     time.Time `json:"periodStart"`
	PeriodEnd       time.Time `json:"periodEnd"`
}

// IngestUsageEvent records a single usage event with idempotency handling.
// If the event has an idempotency key that already exists, it is silently skipped.
func IngestUsageEvent(db *datastore.Datastore, meterId, userId string, value int64, idempotencyKey string, ts time.Time, dimensions map[string]interface{}) (*meter.MeterEvent, bool, error) {
	if meterId == "" {
		return nil, false, fmt.Errorf("meterId is required")
	}
	if userId == "" {
		return nil, false, fmt.Errorf("userId is required")
	}

	// Dedup check
	if idempotencyKey != "" {
		rootKey := db.NewKey("synckey", "", 1, nil)
		existing := make([]*meter.MeterEvent, 0, 1)
		q := meter.QueryEvents(db).Ancestor(rootKey).
			Filter("Idempotency=", idempotencyKey).
			Limit(1)
		if _, err := q.GetAll(&existing); err == nil && len(existing) > 0 {
			return existing[0], true, nil // duplicate
		}
	}

	evt := meter.NewEvent(db)
	evt.MeterId = meterId
	evt.UserId = userId
	evt.Value = value
	evt.Idempotency = idempotencyKey
	evt.Dimensions = dimensions

	if ts.IsZero() {
		evt.Timestamp = time.Now()
	} else {
		evt.Timestamp = ts
	}

	if err := evt.Create(); err != nil {
		return nil, false, fmt.Errorf("failed to create meter event: %w", err)
	}

	return evt, false, nil
}

// IngestUsageEventBatch records multiple usage events, skipping duplicates.
// Returns the number of new events created and the total processed.
func IngestUsageEventBatch(db *datastore.Datastore, events []struct {
	MeterId     string
	UserId      string
	Value       int64
	Idempotency string
	Timestamp   time.Time
	Dimensions  map[string]interface{}
}) (created int, duplicates int, err error) {
	for _, e := range events {
		_, isDup, err := IngestUsageEvent(db, e.MeterId, e.UserId, e.Value, e.Idempotency, e.Timestamp, e.Dimensions)
		if err != nil {
			return created, duplicates, err
		}
		if isDup {
			duplicates++
		} else {
			created++
		}
	}
	return created, duplicates, nil
}

// GetUsageSummary returns aggregated usage for a meter+user over a period.
func GetUsageSummary(db *datastore.Datastore, meterId, userId string, periodStart, periodEnd time.Time) (*UsageSummary, error) {
	m := meter.New(db)
	if err := m.GetById(meterId); err != nil {
		return nil, fmt.Errorf("meter not found: %w", err)
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	events := make([]*meter.MeterEvent, 0)
	q := meter.QueryEvents(db).Ancestor(rootKey).
		Filter("MeterId=", meterId)

	if userId != "" {
		q = q.Filter("UserId=", userId)
	}
	if !periodStart.IsZero() {
		q = q.Filter("Timestamp>=", periodStart)
	}
	if !periodEnd.IsZero() {
		q = q.Filter("Timestamp<=", periodEnd)
	}

	if _, err := q.GetAll(&events); err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}

	var value int64
	var count int64
	for _, evt := range events {
		count++
		switch m.AggregationType {
		case meter.AggSum:
			value += evt.Value
		case meter.AggCount:
			value++
		case meter.AggLast:
			value = evt.Value
		default:
			value += evt.Value
		}
	}

	return &UsageSummary{
		MeterId:         meterId,
		MeterName:       m.Name,
		AggregationType: string(m.AggregationType),
		Value:           value,
		EventCount:      count,
		PeriodStart:     periodStart,
		PeriodEnd:       periodEnd,
	}, nil
}

// AggregateItemUsage aggregates usage for a single subscription item,
// respecting watermarks to prevent double-invoicing. Only events after
// the last watermark's timestamp are included.
func AggregateItemUsage(db *datastore.Datastore, item *subscriptionitem.SubscriptionItem, periodStart, periodEnd time.Time) ([]billinginvoice.LineItem, int64, error) {
	if item.MeterId == "" {
		return nil, 0, fmt.Errorf("subscription item has no meter")
	}

	// Load the meter
	m := meter.New(db)
	if err := m.GetById(item.MeterId); err != nil {
		return nil, 0, fmt.Errorf("meter not found: %w", err)
	}

	// Check for existing watermark to determine start point
	effectiveStart := periodStart
	rootKey := db.NewKey("synckey", "", 1, nil)

	watermarks := make([]*usagewatermark.UsageWatermark, 0)
	wq := usagewatermark.Query(db).Ancestor(rootKey).
		Filter("SubscriptionItemId=", item.Id()).
		Filter("MeterId=", item.MeterId)

	if _, err := wq.GetAll(&watermarks); err == nil && len(watermarks) > 0 {
		// Find the latest watermark
		for _, wm := range watermarks {
			if wm.LastEventTimestamp.After(effectiveStart) {
				effectiveStart = wm.LastEventTimestamp
			}
		}
	}

	// Query events after the watermark
	events := make([]*meter.MeterEvent, 0)
	eq := meter.QueryEvents(db).Ancestor(rootKey).
		Filter("MeterId=", item.MeterId)

	if !effectiveStart.IsZero() {
		eq = eq.Filter("Timestamp>", effectiveStart)
	}
	if !periodEnd.IsZero() {
		eq = eq.Filter("Timestamp<=", periodEnd)
	}

	if _, err := eq.GetAll(&events); err != nil {
		return nil, 0, fmt.Errorf("failed to query events: %w", err)
	}

	if len(events) == 0 {
		return nil, 0, nil
	}

	// Aggregate
	var quantity int64
	var lastTimestamp time.Time
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
		if evt.Timestamp.After(lastTimestamp) {
			lastTimestamp = evt.Timestamp
		}
	}

	// Apply pricing rule
	rules := make([]*pricingrule.PricingRule, 0)
	rq := pricingrule.Query(db).Ancestor(rootKey).
		Filter("MeterId=", item.MeterId)
	if _, err := rq.GetAll(&rules); err == nil && len(rules) > 0 {
		rule := rules[0]
		cost := rule.CalculateCost(quantity)

		lineItems := []billinginvoice.LineItem{{
			Id:          "li_item_" + item.Id(),
			Type:        billinginvoice.LineUsage,
			Description: m.Name + " usage",
			MeterId:     m.Id(),
			Quantity:    quantity,
			UnitPrice:   rule.UnitPrice,
			Amount:      cost,
			Currency:    m.Currency,
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
		}}

		return lineItems, cost, nil
	}

	return nil, 0, nil
}

// CreateWatermark records a watermark after usage has been invoiced,
// preventing those events from being counted again.
func CreateWatermark(db *datastore.Datastore, subscriptionItemId, meterId, invoiceId string, periodStart, periodEnd time.Time, aggregatedValue, eventCount int64, lastEventTimestamp time.Time) (*usagewatermark.UsageWatermark, error) {
	wm := usagewatermark.New(db)
	wm.SubscriptionItemId = subscriptionItemId
	wm.MeterId = meterId
	wm.InvoiceId = invoiceId
	wm.PeriodStart = periodStart
	wm.PeriodEnd = periodEnd
	wm.AggregatedValue = aggregatedValue
	wm.EventCount = eventCount
	wm.LastEventTimestamp = lastEventTimestamp

	if err := wm.Create(); err != nil {
		return nil, fmt.Errorf("failed to create watermark: %w", err)
	}

	return wm, nil
}

// CheckThreshold checks whether usage for a subscription item has exceeded
// a given threshold. Returns whether the threshold is exceeded and the
// current usage value.
func CheckThreshold(db *datastore.Datastore, item *subscriptionitem.SubscriptionItem, threshold int64) (exceeded bool, currentValue int64, err error) {
	if item.MeterId == "" {
		return false, 0, fmt.Errorf("subscription item has no meter")
	}

	m := meter.New(db)
	if err := m.GetById(item.MeterId); err != nil {
		return false, 0, fmt.Errorf("meter not found: %w", err)
	}

	// Get all events for this meter (no period restriction for threshold checks)
	rootKey := db.NewKey("synckey", "", 1, nil)
	events := make([]*meter.MeterEvent, 0)
	eq := meter.QueryEvents(db).Ancestor(rootKey).
		Filter("MeterId=", item.MeterId)

	if _, err := eq.GetAll(&events); err != nil {
		return false, 0, fmt.Errorf("failed to query events: %w", err)
	}

	var value int64
	for _, evt := range events {
		switch m.AggregationType {
		case meter.AggSum:
			value += evt.Value
		case meter.AggCount:
			value++
		case meter.AggLast:
			value = evt.Value
		default:
			value += evt.Value
		}
	}

	return value >= threshold, value, nil
}
