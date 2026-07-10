package engine

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRenewSubscription_IdempotentPerPeriod proves the money-path invariant:
// re-running a billing cycle for the SAME (subscription, period) generates
// EXACTLY ONE invoice, and that invoice carries a sequential number. This is
// the exact case a PastDue subscription hits on every cycle — its period never
// advances while collection fails, so without the idempotency guard each cycle
// would mint a duplicate, unnumbered invoice.
func TestRenewSubscription_IdempotentPerPeriod(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	db := datastore.New(c)
	db.SetNamespace("renew-idem")

	now := time.Now()
	sub := subscription.New(db)
	sub.UserId = "renew-idem/alice"
	sub.PlanId = "plan_pro"
	sub.Plan = plan.Plan{
		Name:          "Pro",
		Price:         currency.Cents(2000), // $20
		Currency:      currency.USD,
		Interval:      types.Monthly,
		IntervalCount: 1,
	}
	sub.Status = subscription.Active
	sub.PeriodStart = now.AddDate(0, -1, 0)
	sub.PeriodEnd = now.AddDate(0, 0, -1) // period already elapsed
	if err := sub.Create(); err != nil {
		t.Fatalf("create sub: %v", err)
	}

	// No balance and no credits (burnCredits=nil) → collection fails → the sub
	// goes PastDue and the period stays fixed.
	inv1, res1, err := RenewSubscription(context.Background(), db, sub, nil)
	if err != nil {
		t.Fatalf("first renew: %v", err)
	}
	if res1 == nil || res1.Success {
		t.Fatalf("expected a non-nil FAILED collection (no funds), got %+v", res1)
	}
	if inv1.Number == 0 || inv1.NumberStr == "" {
		t.Fatalf("first invoice is not numbered: Number=%d NumberStr=%q", inv1.Number, inv1.NumberStr)
	}
	if sub.Status != subscription.PastDue {
		t.Fatalf("sub status = %s, want past_due (period must NOT advance on failed collection)", sub.Status)
	}

	// Re-run the SAME period. Must reuse inv1 — never mint a second invoice.
	inv2, res2, err := RenewSubscription(context.Background(), db, sub, nil)
	if err != nil {
		t.Fatalf("second renew: %v", err)
	}
	if res2 == nil {
		t.Fatalf("second renew returned a nil result (callers dereference result.Success)")
	}
	if inv2.Id() != inv1.Id() {
		t.Fatalf("second renew returned a DIFFERENT invoice: %s vs %s (duplicate!)", inv2.Id(), inv1.Id())
	}

	// The datastore must hold EXACTLY ONE billing invoice for this org.
	rootKey := db.NewKey("synckey", "", 1, nil)
	all := make([]*billinginvoice.BillingInvoice, 0)
	if _, err := billinginvoice.Query(db).Ancestor(rootKey).GetAll(&all); err != nil {
		t.Fatalf("query invoices: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("invoice count = %d, want 1 — no duplicate invoice per period", len(all))
	}
	if all[0].Number == 0 || all[0].NumberStr == "" {
		t.Fatalf("persisted invoice not numbered: Number=%d NumberStr=%q", all[0].Number, all[0].NumberStr)
	}
	t.Logf("PROOF: 2 renews of the same period -> 1 invoice %s (#%d), sub=%s",
		all[0].NumberStr, all[0].Number, sub.Status)
}

// TestRenewSubscription_NumbersDistinctPeriods proves numbering increments
// across distinct periods: a successful first period (which advances the sub)
// followed by a second renewal produces two invoices numbered 1 then 2.
func TestRenewSubscription_NumbersDistinctPeriods(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	db := datastore.New(c)
	db.SetNamespace("renew-number")

	now := time.Now()
	sub := subscription.New(db)
	sub.UserId = "renew-number/dave"
	sub.PlanId = "plan_free"
	sub.Plan = plan.Plan{
		Name:          "Zero",
		Price:         currency.Cents(0), // $0 plan → AmountDue 0 → collection trivially succeeds
		Currency:      currency.USD,
		Interval:      types.Monthly,
		IntervalCount: 1,
	}
	sub.Status = subscription.Active
	sub.PeriodStart = now.AddDate(0, -2, 0)
	sub.PeriodEnd = now.AddDate(0, -1, 0)
	if err := sub.Create(); err != nil {
		t.Fatalf("create sub: %v", err)
	}

	inv1, res1, err := RenewSubscription(context.Background(), db, sub, nil)
	if err != nil {
		t.Fatalf("first renew: %v", err)
	}
	if !res1.Success {
		t.Fatalf("zero-amount invoice should collect successfully, got %+v", res1)
	}
	if inv1.Number != 1 {
		t.Fatalf("first invoice number = %d, want 1", inv1.Number)
	}

	// Period advanced on success; a second renewal is a NEW period → invoice #2.
	inv2, _, err := RenewSubscription(context.Background(), db, sub, nil)
	if err != nil {
		t.Fatalf("second renew: %v", err)
	}
	if inv2.Number != 2 {
		t.Fatalf("second invoice number = %d, want 2 (per-org sequential)", inv2.Number)
	}
	if inv2.Id() == inv1.Id() {
		t.Fatalf("distinct periods must yield distinct invoices")
	}
}
