package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/types"
)

// StartSubscription initializes a new subscription: sets the initial state,
// computes period dates, and handles trial logic.
func StartSubscription(sub *subscription.Subscription, p *plan.Plan) {
	now := time.Now()
	sub.Plan = *p
	sub.PlanId = p.Id()
	sub.Start = now

	if p.TrialPeriodDays > 0 {
		sub.Status = subscription.Trialing
		sub.TrialStart = now
		sub.TrialEnd = now.AddDate(0, 0, p.TrialPeriodDays)
		sub.PeriodStart = sub.TrialEnd
		sub.PeriodEnd = advancePeriod(sub.TrialEnd, p)
	} else {
		sub.Status = subscription.Active
		sub.PeriodStart = now
		sub.PeriodEnd = advancePeriod(now, p)
	}
}

// RenewSubscription generates an invoice for the current billing period
// and attempts to collect payment. Returns the invoice and collection result.
//
// It is idempotent per (subscription, period): a PastDue renewal re-runs the
// SAME period (the period only advances on a successful collection), so this
// must NEVER mint a second invoice for a period already invoiced. If an
// invoice for this exact period already exists it is returned as-is (no
// duplicate, no re-charge); retrying collection on an unpaid invoice is the
// dunning workflow's job (billing/workflows/dunning.go), not this generator's.
func RenewSubscription(ctx context.Context, db *datastore.Datastore, sub *subscription.Subscription, burnCredits CreditBurner) (*billinginvoice.BillingInvoice, *CollectionResult, error) {
	// Idempotency guard: one invoice per (subscription, period).
	existing, err := findInvoiceForPeriod(db, sub)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to look up existing invoice for period: %w", err)
	}
	if existing != nil {
		return existing, resultFromInvoice(existing), nil
	}

	// Generate a fresh, sequentially-numbered invoice for this period.
	inv, err := buildPeriodInvoice(db, sub)
	if err != nil {
		return inv, nil, err
	}

	// Attempt collection
	result, err := CollectInvoice(ctx, db, inv, burnCredits)
	if err != nil {
		return inv, result, fmt.Errorf("collection error: %w", err)
	}

	// Update invoice after collection
	if err := inv.Update(); err != nil {
		return inv, result, fmt.Errorf("failed to update invoice: %w", err)
	}

	// Update subscription period
	if result.Success {
		sub.CurrentInvoiceId = inv.Id()
		sub.PeriodStart = sub.PeriodEnd
		sub.PeriodEnd = advancePeriod(sub.PeriodEnd, &sub.Plan)
	} else {
		sub.Status = subscription.PastDue
	}

	return inv, result, nil
}

// buildPeriodInvoice constructs, numbers, finalizes and persists a new invoice
// for the subscription's current period. The invoice number is a sequential
// per-org counter, mirroring the credit-note numbering in refunds.go.
func buildPeriodInvoice(db *datastore.Datastore, sub *subscription.Subscription) (*billinginvoice.BillingInvoice, error) {
	inv := billinginvoice.New(db)
	inv.UserId = sub.UserId
	inv.SubscriptionId = sub.Id()
	inv.PeriodStart = sub.PeriodStart
	inv.PeriodEnd = sub.PeriodEnd
	inv.Currency = sub.Plan.Currency

	// Add subscription line item (flat plan fee)
	if sub.Plan.Price > 0 {
		inv.LineItems = append(inv.LineItems, billinginvoice.LineItem{
			Id:          "li_plan_" + sub.PlanId,
			Type:        billinginvoice.LineSubscription,
			Description: sub.Plan.Name + " subscription",
			PlanId:      sub.PlanId,
			PlanName:    sub.Plan.Name,
			Amount:      int64(sub.Plan.Price),
			Currency:    sub.Plan.Currency,
			PeriodStart: sub.PeriodStart,
			PeriodEnd:   sub.PeriodEnd,
		})
	}

	// Add usage line items (non-fatal: an aggregation error yields no usage).
	if usageItems, _, err := AggregateUsage(db, sub.UserId, sub.PeriodStart, sub.PeriodEnd); err == nil {
		inv.LineItems = append(inv.LineItems, usageItems...)
	}

	// Calculate totals
	inv.RecalculateSubtotal()

	// Assign a sequential per-org invoice number BEFORE persisting.
	assignInvoiceNumber(db, inv)

	// Finalize (draft -> open)
	if err := inv.Finalize(); err != nil {
		return inv, fmt.Errorf("failed to finalize invoice: %w", err)
	}

	// Persist invoice
	if err := inv.Create(); err != nil {
		return inv, fmt.Errorf("failed to create invoice: %w", err)
	}

	return inv, nil
}

// findInvoiceForPeriod returns the existing invoice for this subscription's
// exact billing period, or nil if none exists. Periods are months/years apart,
// so PeriodStart/PeriodEnd are matched at second precision to be robust against
// sub-second serialization differences across the storage round-trip.
func findInvoiceForPeriod(db *datastore.Datastore, sub *subscription.Subscription) (*billinginvoice.BillingInvoice, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	existing := make([]*billinginvoice.BillingInvoice, 0)
	q := billinginvoice.Query(db).Ancestor(rootKey).
		Filter("SubscriptionId=", sub.Id()).
		Filter("UserId=", sub.UserId)
	if _, err := q.GetAll(&existing); err != nil {
		return nil, err
	}
	for _, inv := range existing {
		if inv.PeriodStart.Unix() == sub.PeriodStart.Unix() &&
			inv.PeriodEnd.Unix() == sub.PeriodEnd.Unix() {
			return inv, nil
		}
	}
	return nil, nil
}

// assignInvoiceNumber sets a sequential per-org invoice number = (count of
// existing billing invoices) + 1. Mirrors the credit-note numbering pattern in
// refunds.go; if the count query fails it falls back to 1.
func assignInvoiceNumber(db *datastore.Datastore, inv *billinginvoice.BillingInvoice) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	existing := make([]*billinginvoice.BillingInvoice, 0)
	if _, err := billinginvoice.Query(db).Ancestor(rootKey).GetAll(&existing); err == nil {
		inv.SetNumber(len(existing) + 1)
	} else {
		inv.SetNumber(1)
	}
}

// resultFromInvoice synthesizes a collection result from an invoice's persisted
// state — used when RenewSubscription returns an already-generated invoice so
// callers (e.g. the billing cycle) get a non-nil result reflecting whether the
// period is settled.
func resultFromInvoice(inv *billinginvoice.BillingInvoice) *CollectionResult {
	return &CollectionResult{
		Success:       inv.Status == billinginvoice.Paid,
		CreditUsed:    inv.CreditApplied,
		AmountCharged: inv.AmountPaid,
	}
}

// TransitionTrialToActive moves a trialing subscription to active.
func TransitionTrialToActive(sub *subscription.Subscription) error {
	if sub.Status != subscription.Trialing {
		return fmt.Errorf("subscription is not trialing, current status: %s", sub.Status)
	}
	sub.Status = subscription.Active
	return nil
}

// CancelSubscription cancels a subscription, either immediately or at period end.
func CancelSubscription(sub *subscription.Subscription, atPeriodEnd bool) error {
	if sub.Status == subscription.Canceled {
		return fmt.Errorf("subscription is already canceled")
	}

	now := time.Now()

	if atPeriodEnd {
		sub.EndCancel = true
		sub.CanceledAt = now
	} else {
		sub.Status = subscription.Canceled
		sub.Canceled = true
		sub.CanceledAt = now
		sub.Ended = now
	}

	return nil
}

// ReactivateSubscription reverses a pending cancellation.
func ReactivateSubscription(sub *subscription.Subscription) error {
	if sub.Status == subscription.Canceled && !sub.Ended.IsZero() {
		return fmt.Errorf("cannot reactivate a fully ended subscription")
	}

	sub.EndCancel = false
	sub.Canceled = false
	sub.CanceledAt = time.Time{}

	if sub.Status == subscription.Canceled {
		sub.Status = subscription.Active
	}

	return nil
}

// ChangePlan updates a subscription to a new plan. If prorate is true,
// a proration line item will be added to the current period's invoice.
func ChangePlan(sub *subscription.Subscription, newPlan *plan.Plan, prorate bool) (*billinginvoice.LineItem, error) {
	oldPlan := sub.Plan
	sub.Plan = *newPlan
	sub.PlanId = newPlan.Id()

	if !prorate {
		return nil, nil
	}

	// Calculate proration
	now := time.Now()
	totalDays := sub.PeriodEnd.Sub(sub.PeriodStart).Hours() / 24
	remainingDays := sub.PeriodEnd.Sub(now).Hours() / 24

	if totalDays <= 0 {
		return nil, nil
	}

	fraction := remainingDays / totalDays

	// Credit for unused portion of old plan
	oldCredit := int64(float64(oldPlan.Price) * fraction)
	// Charge for remaining portion of new plan
	newCharge := int64(float64(newPlan.Price) * fraction)

	net := newCharge - oldCredit

	item := &billinginvoice.LineItem{
		Id:          fmt.Sprintf("li_proration_%d", now.Unix()),
		Type:        billinginvoice.LineProration,
		Description: fmt.Sprintf("Proration: %s -> %s", oldPlan.Name, newPlan.Name),
		PlanId:      newPlan.Id(),
		PlanName:    newPlan.Name,
		Amount:      net,
		Currency:    newPlan.Currency,
		PeriodStart: now,
		PeriodEnd:   sub.PeriodEnd,
	}

	return item, nil
}

// advancePeriod computes the next period end date based on the plan interval.
func advancePeriod(from time.Time, p *plan.Plan) time.Time {
	count := p.IntervalCount
	if count <= 0 {
		count = 1
	}

	switch p.Interval {
	case types.Monthly:
		return from.AddDate(0, count, 0)
	case types.Yearly:
		return from.AddDate(count, 0, 0)
	default:
		// Default to monthly
		return from.AddDate(0, count, 0)
	}
}
