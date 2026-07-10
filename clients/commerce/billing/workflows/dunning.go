package workflows

import (
	"time"

	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// DunningParams configures the dunning retry workflow.
type DunningParams struct {
	OrgName        string `json:"orgName"`
	SubscriptionId string `json:"subscriptionId"`
	InvoiceId      string `json:"invoiceId"`
	MaxRetries     int    `json:"maxRetries"`
}

// Dunning retry schedule: delays between attempts.
var dunningSchedule = []time.Duration{
	0,               // Attempt 1: immediate (already failed)
	24 * time.Hour,  // Attempt 2: +1 day
	72 * time.Hour,  // Attempt 3: +3 days
	168 * time.Hour, // Attempt 4: +7 days
}

// DunningWorkflow retries payment collection on a failed invoice.
// If all retries fail, the invoice is marked uncollectible and the
// subscription transitions to unpaid.
func DunningWorkflow(ctx workflow.Context, params DunningParams) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting dunning", "invoiceId", params.InvoiceId, "maxRetries", params.MaxRetries)

	var activities *BillingActivities
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})

	paymentCh := workflow.GetSignalChannel(ctx, SignalPaymentReceived)

	maxAttempts := params.MaxRetries
	if maxAttempts <= 0 || maxAttempts > len(dunningSchedule) {
		maxAttempts = len(dunningSchedule)
	}

	for attempt := 1; attempt < maxAttempts; attempt++ {
		delay := dunningSchedule[attempt]

		if delay > 0 {
			timerCtx, cancelTimer := workflow.WithCancel(ctx)
			timerFuture := workflow.NewTimer(timerCtx, delay)

			s := workflow.NewSelector(ctx)
			timerFired := false

			s.AddFuture(timerFuture, func(f workflow.Future) {
				timerFired = true
			})

			s.AddReceive(paymentCh, func(ch workflow.ReceiveChannel, more bool) {
				var sig struct{}
				ch.Receive(ctx, &sig)
				cancelTimer()
			})

			s.Select(ctx)

			if !timerFired {
				logger.Info("Dunning resolved by external payment")
				return nil
			}
		}

		// Retry collection
		var result CollectionActivityResult
		err := workflow.ExecuteActivity(actCtx, activities.CollectInvoiceActivity, CollectInvoiceParams{
			OrgName:   params.OrgName,
			InvoiceId: params.InvoiceId,
		}).Get(ctx, &result)

		if err != nil {
			logger.Error("Dunning collection attempt failed", "attempt", attempt, "error", err)
			continue
		}

		if result.Success {
			logger.Info("Dunning collection succeeded", "attempt", attempt)
			_ = workflow.ExecuteActivity(actCtx, activities.TransitionSubscriptionActivity, TransitionParams{
				OrgName:        params.OrgName,
				SubscriptionId: params.SubscriptionId,
				NewStatus:      "active",
			}).Get(ctx, nil)
			return nil
		}

		logger.Warn("Dunning attempt failed", "attempt", attempt, "remaining", maxAttempts-attempt-1)
	}

	// All retries exhausted
	logger.Error("All dunning attempts exhausted, marking uncollectible")

	_ = workflow.ExecuteActivity(actCtx, activities.MarkUncollectibleActivity, MarkUncollectibleParams{
		OrgName:   params.OrgName,
		InvoiceId: params.InvoiceId,
	}).Get(ctx, nil)

	_ = workflow.ExecuteActivity(actCtx, activities.TransitionSubscriptionActivity, TransitionParams{
		OrgName:        params.OrgName,
		SubscriptionId: params.SubscriptionId,
		NewStatus:      "unpaid",
	}).Get(ctx, nil)

	return nil
}
