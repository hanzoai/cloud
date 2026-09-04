// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// plan_rpc.go — what a subject is on and what is left of it, over the internal
// plane: the tier their subscriptions confer, the month measured against the
// plan, and the subscription rows themselves.
//
// THE TIER OVERRIDE IS NOT HERE, and that is the point of the split. A caller
// can NAME a tier rather than earn one, through an X-Tier header or an explicit
// ?tier= — both request facts, and both a MINT, admitted only for a caller that
// may mint. The edge can see the credential and decides; this side derives from
// the store and takes the answer as a value. A core that read those would be
// honouring a claim nobody proved.

import (
	"context"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercetier "github.com/hanzoai/commerce/billing/tier"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// exposePlan publishes the tier, rollup and subscription ops. Mount calls it.
func exposePlan() {
	zip.Post[client.TierIn, client.Tier](cloud.Plane(), "/billing/tier", planeTier,
		zip.WithOperationID(client.BillingTier),
		zip.WithSummary("What a subject's plan allows and what they can spend"))
	zip.Post[client.RollupIn, client.Rollup](cloud.Plane(), "/billing/rollup", planeRollup,
		zip.WithOperationID(client.BillingRollup),
		zip.WithSummary("A subject's month against their plan, and the wallet beside it"))
	zip.Post[client.SubsIn, client.Subscriptions](cloud.Plane(), "/billing/subscriptions", planeSubscriptions,
		zip.WithOperationID(client.BillingSubscriptions),
		zip.WithSummary("The plans a subject holds"))
	zip.Post[client.SubscriptionRef, client.Subscription](cloud.Plane(), "/billing/subscription/cancel", planeSubscriptionCancel,
		zip.WithOperationID(client.BillingSubscriptionCancel),
		zip.WithSummary("End a subscription"))
	zip.Post[client.SubscriptionRef, client.Subscription](cloud.Plane(), "/billing/subscription/reactivate", planeSubscriptionReactivate,
		zip.WithOperationID(client.BillingSubscriptionReactivate),
		zip.WithSummary("Put a canceled subscription back on its plan"))
}

// Answers which tier a subject is on, what it allows, and what they can spend.
//
// The tier is DERIVED from their own active subscriptions unless the edge
// supplied one it was entitled to mint. A tier that cannot be read is an ERROR
// rather than Free: the router in front of the models maps any non-2xx to Free,
// so answering Free from a question nobody could answer would pin every paying
// customer to the most restrictive row with nothing anywhere to find.
//
// effectiveAvailable is the only figure a gate compares against zero — the
// others are its parts, three sources of one spend rather than three balances to
// add up a second time.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTier(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
	org, err := orgOf(ctx, "tier")
	if err != nil {
		return nil, err
	}
	name := commercetier.Name(in.Tier)
	if in.Tier == "" {
		// No minted override, so the tier is whatever this subject's own
		// subscriptions confer. A store hiccup is returned rather than answered
		// as Free — see above.
		derived, derr := commercebilling.TierOf(ctx, org, in.Subject)
		if derr != nil {
			return nil, zip.Errorf(500, "failed to resolve tier")
		}
		name = derived
	}
	view, verr := commercebilling.ReadTier(ctx, org, in.Subject, name)
	if verr != nil {
		return nil, zip.Errorf(500, "failed to query balance")
	}
	return &client.Tier{
		User:    view.User,
		Tier:    tierLimits(view),
		Balance: tierBalance(view),
		Windows: windows(view.Windows),
	}, nil
}

// Answers a subject's month: what the plan includes, what has been consumed
// against it, and the wallet beside it.
//
// The two blocks stay SEPARATE because they are separate monies — one was sold
// with the plan, one was bought with a card — and their sum is not a number
// anyone holds. Nothing here adds them, and a reader that did would be inventing
// a balance.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRollup(ctx context.Context, in *client.RollupIn) (*client.Rollup, error) {
	org, err := orgOf(ctx, "rollup")
	if err != nil {
		return nil, err
	}
	view, verr := commercebilling.ReadRollup(ctx, org, in.Subject, in.Plan, time.Now())
	if verr != nil {
		return nil, zip.Errorf(500, "failed to query balance")
	}
	return &client.Rollup{
		User:     view.User,
		Plan:     view.Plan,
		Currency: view.Currency,
		Period:   view.Period,
		Windows:  windows(view.Windows),
		Included: client.RollupAllotment{
			MonthlyCents:   view.Included.MonthlyCents,
			GrantedCents:   view.Included.GrantedCents,
			ConsumedCents:  view.Included.ConsumedCents,
			RemainingCents: view.Included.RemainingCents,
		},
		ConsumedCents: view.ConsumedCents,
		OverageCents:  view.OverageCents,
		Balance: client.RollupBalance{
			BalanceCents:   view.Balance.BalanceCents,
			HoldsCents:     view.Balance.HoldsCents,
			AvailableCents: view.Balance.AvailableCents,
		},
	}, nil
}

// Lists the plans a subject holds, with the count beside them.
//
// It is the CUSTOMER's view of the same rows FinanceSubs answers for an
// operator's revenue board — one store, one core, one projection, two audiences.
// Folding the two would make the customer's page inherit the board's fields or
// the board inherit the customer's.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptions(ctx context.Context, in *client.SubsIn) (*client.Subscriptions, error) {
	org, err := orgOf(ctx, "subscriptions")
	if err != nil {
		return nil, err
	}
	rows, rerr := commercebilling.Subscriptions(ctx, org, in.UserID, in.Status)
	if rerr != nil {
		return nil, zip.Errorf(500, "failed to list subscriptions")
	}
	out := make([]client.Subscription, 0, len(rows))
	for i := range rows {
		out = append(out, subscriptionRow(&rows[i]))
	}
	return &client.Subscriptions{Rows: out, Count: len(out)}, nil
}

// Ends a subscription — at the end of the period already paid for, or at once.
//
// The lifecycle belongs to the engine, so a move it will not make comes back as
// the caller's own refusal rather than being re-decided here: one state machine,
// in one place. A subscription from another org is not found rather than
// refused, so an id cannot be probed for existence.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionCancel(ctx context.Context, in *client.SubscriptionRef) (*client.Subscription, error) {
	org, err := orgOf(ctx, "cancel subscription")
	if err != nil {
		return nil, err
	}
	sub, serr := commercebilling.CancelSubscription(ctx, org, in.ID, in.AtPeriodEnd)
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to cancel subscription")
	}
	row := subscriptionRow(sub)
	return &row, nil
}

// Puts a canceled subscription back on its plan.
//
// It is the act least likely to come from a browser — what asks for it is
// usually a recovered payment method or a support tool — which is most of the
// argument for it being answerable by name at all.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionReactivate(ctx context.Context, in *client.SubscriptionRef) (*client.Subscription, error) {
	org, err := orgOf(ctx, "reactivate subscription")
	if err != nil {
		return nil, err
	}
	sub, serr := commercebilling.ReactivateSubscription(ctx, org, in.ID)
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to reactivate subscription")
	}
	row := subscriptionRow(sub)
	return &row, nil
}

// subscriptionFault maps the store's refusals onto the statuses this family has
// always answered with. A move the lifecycle will not make is the CALLER'S
// mistake (400) carrying the engine's own sentence; a row that is not there is
// 404 whether the id names nothing or names another org's.
func subscriptionFault(err error, generic string) error {
	switch {
	case commercebilling.IsSubscriptionNotFound(err):
		return zip.Errorf(404, "subscription not found")
	case commercebilling.IsSubscriptionRefused(err):
		return zip.Errorf(400, "%v", err)
	}
	return zip.Errorf(500, "%s", generic)
}

// subscriptionRow moves one subscription onto the wire.
//
// The four dates a subscription may not have cross as TEXT and are empty when
// absent, because the key is dropped on a CONDITION — a trial was opened, a
// cancel happened, the row ended — and not because a value is zero.
func subscriptionRow(s *commercebilling.Subscription) client.Subscription {
	return client.Subscription{
		ID:                   s.ID,
		UserID:               s.UserID,
		PlanID:               s.PlanID,
		Status:               s.Status,
		Quantity:             s.Quantity,
		CurrentPeriodStart:   stamp(s.CurrentPeriodStart),
		CurrentPeriodEnd:     stamp(s.CurrentPeriodEnd),
		CancelAtPeriodEnd:    s.CancelAtPeriodEnd,
		MRRCents:             s.MRRCents,
		ProviderType:         s.ProviderType,
		DefaultPaymentMethod: s.DefaultPaymentMethod,
		Plan: client.SubscriptionPlan{
			ID:       s.Plan.ID,
			Name:     s.Plan.Name,
			Price:    s.Plan.Price,
			Currency: s.Plan.Currency,
			Interval: s.Plan.Interval,
		},
		CreatedAt:  stamp(s.CreatedAt),
		UpdatedAt:  stamp(s.UpdatedAt),
		TrialStart: stampOf(s.TrialStart),
		TrialEnd:   stampOf(s.TrialEnd),
		CanceledAt: stampOf(s.CanceledAt),
		EndedAt:    stampOf(s.EndedAt),
	}
}

// stampOf renders an optional time. A nil pointer is the absent key, which is
// what the pointer meant on the way in.
func stampOf(t *time.Time) string {
	if t == nil {
		return ""
	}
	return stamp(*t)
}

// tierLimits and tierBalance move the two halves of a tier onto the wire. They
// are separate functions because they answer different questions — what may this
// caller use, and what is left to use it with — and reading them together is the
// reader's job, not this side's.
func tierLimits(v *commercebilling.TierView) client.TierLimits {
	return client.TierLimits{
		Name:              string(v.Tier.Name),
		DisplayName:       v.Tier.DisplayName,
		MaxAgents:         v.Tier.MaxAgents,
		DailyCreditsCents: v.Tier.DailyCreditsCents,
		AllowedModels:     v.Tier.AllowedModels,
		UnlimitedAgents:   v.Tier.UnlimitedAgents,
	}
}

func tierBalance(v *commercebilling.TierView) client.TierBalance {
	return client.TierBalance{
		Currency:           string(v.Balance.Currency),
		PrepaidAvailable:   int64(v.Balance.PrepaidAvailable),
		CreditsRemaining:   int64(v.Balance.CreditsRemaining),
		DailyRemaining:     int64(v.Balance.DailyRemaining),
		EffectiveAvailable: int64(v.Balance.EffectiveAvailable),
	}
}

// windows moves a plan's nested request bounds onto the wire. A window with
// limit 0 declares NO bound at that span rather than a bound of zero, and it
// travels as it is so a reader skips it instead of reporting it exhausted.
func windows(in []commercebilling.Window) []client.Window {
	out := make([]client.Window, 0, len(in))
	for _, w := range in {
		out = append(out, client.Window{
			Span: w.Span, Limit: w.Limit, Used: w.Used,
			Remaining: w.Remaining, Resets: w.Resets,
		})
	}
	return out
}
