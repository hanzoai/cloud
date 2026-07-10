package workflows

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
)

// Activity parameter and result types

type TransitionParams struct {
	OrgName        string `json:"orgName"`
	SubscriptionId string `json:"subscriptionId"`
	NewStatus      string `json:"newStatus"`
}

type CancelParams struct {
	OrgName        string `json:"orgName"`
	SubscriptionId string `json:"subscriptionId"`
	AtPeriodEnd    bool   `json:"atPeriodEnd"`
}

type ChangePlanParams struct {
	OrgName        string `json:"orgName"`
	SubscriptionId string `json:"subscriptionId"`
	NewPlanId      string `json:"newPlanId"`
	Prorate        bool   `json:"prorate"`
}

type RenewalParams struct {
	OrgName        string `json:"orgName"`
	SubscriptionId string `json:"subscriptionId"`
}

type RenewalResult struct {
	Success       bool      `json:"success"`
	InvoiceId     string    `json:"invoiceId"`
	NextPeriodEnd time.Time `json:"nextPeriodEnd"`
}

type CollectInvoiceParams struct {
	OrgName   string `json:"orgName"`
	InvoiceId string `json:"invoiceId"`
}

type CollectionActivityResult struct {
	Success bool `json:"success"`
}

type MarkUncollectibleParams struct {
	OrgName   string `json:"orgName"`
	InvoiceId string `json:"invoiceId"`
}

// BillingActivities holds dependencies for activity implementations.
type BillingActivities struct {
	// BurnCredits is injected from the billing package to avoid circular imports.
	BurnCredits engine.CreditBurner
}

// TransitionSubscriptionActivity updates a subscription's status.
func (a *BillingActivities) TransitionSubscriptionActivity(ctx context.Context, params TransitionParams) error {
	db := orgDB(ctx, params.OrgName)

	sub := subscription.New(db)
	if err := sub.GetById(params.SubscriptionId); err != nil {
		return fmt.Errorf("subscription not found: %w", err)
	}

	sub.Status = subscription.Status(params.NewStatus)
	return sub.Update()
}

// CancelSubscriptionActivity cancels a subscription.
func (a *BillingActivities) CancelSubscriptionActivity(ctx context.Context, params CancelParams) error {
	db := orgDB(ctx, params.OrgName)

	sub := subscription.New(db)
	if err := sub.GetById(params.SubscriptionId); err != nil {
		return fmt.Errorf("subscription not found: %w", err)
	}

	if err := engine.CancelSubscription(sub, params.AtPeriodEnd); err != nil {
		return err
	}

	return sub.Update()
}

// ChangePlanActivity changes a subscription's plan.
func (a *BillingActivities) ChangePlanActivity(ctx context.Context, params ChangePlanParams) error {
	db := orgDB(ctx, params.OrgName)

	sub := subscription.New(db)
	if err := sub.GetById(params.SubscriptionId); err != nil {
		return fmt.Errorf("subscription not found: %w", err)
	}

	plan := newPlan(db)
	if err := plan.GetById(params.NewPlanId); err != nil {
		return fmt.Errorf("plan not found: %w", err)
	}

	_, err := engine.ChangePlan(sub, plan, params.Prorate)
	if err != nil {
		return err
	}

	return sub.Update()
}

// RenewSubscriptionActivity generates an invoice and attempts collection.
func (a *BillingActivities) RenewSubscriptionActivity(ctx context.Context, params RenewalParams) (*RenewalResult, error) {
	db := orgDB(ctx, params.OrgName)

	sub := subscription.New(db)
	if err := sub.GetById(params.SubscriptionId); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}

	inv, result, err := engine.RenewSubscription(ctx, db, sub, a.BurnCredits)
	if err != nil {
		return nil, err
	}

	if err := sub.Update(); err != nil {
		return nil, fmt.Errorf("failed to update subscription: %w", err)
	}

	return &RenewalResult{
		Success:       result.Success,
		InvoiceId:     inv.Id(),
		NextPeriodEnd: sub.PeriodEnd,
	}, nil
}

// CollectInvoiceActivity attempts to collect payment on an invoice.
func (a *BillingActivities) CollectInvoiceActivity(ctx context.Context, params CollectInvoiceParams) (*CollectionActivityResult, error) {
	db := orgDB(ctx, params.OrgName)

	inv := billinginvoice.New(db)
	if err := inv.GetById(params.InvoiceId); err != nil {
		return nil, fmt.Errorf("invoice not found: %w", err)
	}

	result, err := engine.CollectInvoice(ctx, db, inv, a.BurnCredits)
	if err != nil {
		return nil, err
	}

	if err := inv.Update(); err != nil {
		return nil, fmt.Errorf("failed to update invoice: %w", err)
	}

	return &CollectionActivityResult{Success: result.Success}, nil
}

// MarkUncollectibleActivity marks an invoice as uncollectible.
func (a *BillingActivities) MarkUncollectibleActivity(ctx context.Context, params MarkUncollectibleParams) error {
	db := orgDB(ctx, params.OrgName)

	inv := billinginvoice.New(db)
	if err := inv.GetById(params.InvoiceId); err != nil {
		return fmt.Errorf("invoice not found: %w", err)
	}

	if err := inv.MarkUncollectible(); err != nil {
		return err
	}

	return inv.Update()
}

// orgDB creates a datastore scoped to an org namespace.
func orgDB(ctx context.Context, orgName string) *datastore.Datastore {
	db := datastore.New(ctx)
	if orgName != "" {
		db.SetNamespace(orgName)
	}
	return db
}

// newPlan creates a new plan model for lookup.
func newPlan(db *datastore.Datastore) *plan.Plan {
	return plan.New(db)
}
