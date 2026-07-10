// Package workflows provides Temporal workflow definitions for
// automated recurring billing, subscription lifecycle management,
// and dunning retry logic.
package workflows

import (
	"fmt"
	"time"

	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

const (
	// TaskQueue is the Temporal task queue for billing workflows.
	TaskQueue = "commerce-billing"

	// Signal names
	SignalCancel          = "cancel"
	SignalReactivate      = "reactivate"
	SignalChangePlan      = "change_plan"
	SignalPaymentReceived = "payment_received"
)

// SubscriptionWorkflowParams contains the initial parameters for the workflow.
type SubscriptionWorkflowParams struct {
	OrgName        string    `json:"orgName"`
	SubscriptionId string    `json:"subscriptionId"`
	UserId         string    `json:"userId"`
	PlanId         string    `json:"planId"`
	TrialEnd       time.Time `json:"trialEnd,omitempty"`
	PeriodEnd      time.Time `json:"periodEnd"`
}

// CancelSignal carries cancellation parameters.
type CancelSignal struct {
	AtPeriodEnd bool `json:"atPeriodEnd"`
}

// ChangePlanSignal carries plan change parameters.
type ChangePlanSignal struct {
	NewPlanId string `json:"newPlanId"`
	Prorate   bool   `json:"prorate"`
}

// SubscriptionLifecycleWorkflow manages the entire lifecycle of a subscription.
// One instance per active subscription. Workflow ID: "billing:sub:{subscriptionId}"
func SubscriptionLifecycleWorkflow(ctx workflow.Context, params SubscriptionWorkflowParams) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting subscription lifecycle", "subscriptionId", params.SubscriptionId)

	// Activity stub — methods are resolved by name from registered activities
	var activities *BillingActivities
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	})

	// Phase 1: Handle trial period
	if !params.TrialEnd.IsZero() && params.TrialEnd.After(workflow.Now(ctx)) {
		logger.Info("Subscription in trial", "trialEnd", params.TrialEnd)

		sleepDuration := params.TrialEnd.Sub(workflow.Now(ctx))
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		timerFuture := workflow.NewTimer(timerCtx, sleepDuration)
		signalCh := workflow.GetSignalChannel(ctx, SignalCancel)

		s := workflow.NewSelector(ctx)
		s.AddFuture(timerFuture, func(f workflow.Future) {
			_ = workflow.ExecuteActivity(actCtx, activities.TransitionSubscriptionActivity, TransitionParams{
				OrgName:        params.OrgName,
				SubscriptionId: params.SubscriptionId,
				NewStatus:      "active",
			}).Get(ctx, nil)
		})
		s.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
			var sig CancelSignal
			ch.Receive(ctx, &sig)
			cancelTimer()
		})
		s.Select(ctx)
	}

	// Phase 2: Recurring billing loop
	for {
		now := workflow.Now(ctx)
		sleepDuration := params.PeriodEnd.Sub(now)

		if sleepDuration > 0 {
			timerCtx, cancelTimer := workflow.WithCancel(ctx)
			timerFuture := workflow.NewTimer(timerCtx, sleepDuration)

			cancelCh := workflow.GetSignalChannel(ctx, SignalCancel)
			changePlanCh := workflow.GetSignalChannel(ctx, SignalChangePlan)

			s := workflow.NewSelector(ctx)
			periodEnded := false

			s.AddFuture(timerFuture, func(f workflow.Future) {
				periodEnded = true
			})

			s.AddReceive(cancelCh, func(ch workflow.ReceiveChannel, more bool) {
				var sig CancelSignal
				ch.Receive(ctx, &sig)
				cancelTimer()
				_ = workflow.ExecuteActivity(actCtx, activities.CancelSubscriptionActivity, CancelParams{
					OrgName:        params.OrgName,
					SubscriptionId: params.SubscriptionId,
					AtPeriodEnd:    sig.AtPeriodEnd,
				}).Get(ctx, nil)
			})

			s.AddReceive(changePlanCh, func(ch workflow.ReceiveChannel, more bool) {
				var sig ChangePlanSignal
				ch.Receive(ctx, &sig)
				_ = workflow.ExecuteActivity(actCtx, activities.ChangePlanActivity, ChangePlanParams{
					OrgName:        params.OrgName,
					SubscriptionId: params.SubscriptionId,
					NewPlanId:      sig.NewPlanId,
					Prorate:        sig.Prorate,
				}).Get(ctx, nil)
			})

			s.Select(ctx)

			if !periodEnded {
				continue
			}
		}

		// Period ended — generate invoice and collect
		var result RenewalResult
		err := workflow.ExecuteActivity(actCtx, activities.RenewSubscriptionActivity, RenewalParams{
			OrgName:        params.OrgName,
			SubscriptionId: params.SubscriptionId,
		}).Get(ctx, &result)

		if err != nil {
			logger.Error("Renewal activity failed", "error", err)
			return err
		}

		if result.Success {
			params.PeriodEnd = result.NextPeriodEnd
			logger.Info("Subscription renewed", "nextPeriodEnd", result.NextPeriodEnd)
			continue
		}

		// Payment failed — enter dunning
		logger.Warn("Payment failed, entering dunning", "invoiceId", result.InvoiceId)
		err = workflow.ExecuteChildWorkflow(ctx, DunningWorkflow, DunningParams{
			OrgName:        params.OrgName,
			SubscriptionId: params.SubscriptionId,
			InvoiceId:      result.InvoiceId,
			MaxRetries:     4,
		}).Get(ctx, nil)

		if err != nil {
			logger.Error("Dunning workflow failed", "error", err)
			return err
		}
	}
}

// WorkflowID returns the standard workflow ID for a subscription.
func WorkflowID(subscriptionId string) string {
	return fmt.Sprintf("billing:sub:%s", subscriptionId)
}
