package billing

// subscriptions.go serves the customer's own plan: what they are on, ending it,
// and putting it back.
//
// The rows are commerce's and the lifecycle is its engine's, so a move the
// engine will not make comes back as the caller's own refusal rather than being
// re-decided here. One state machine, in one place — a door that re-checked it
// would be a second opinion about whether a paid period may be cancelled twice.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountSubscriptions registers the subscription family. Called from routes.
func mountSubscriptions(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/subscriptions", o.subscriptions)
	zip.Post(zapp, "/v1/billing/subscriptions/:id/cancel", o.cancelSubscription,
		zip.WithOperationID("cancelSubscription"),
		zip.WithSummary("End a subscription"))
	zip.Post(zapp, "/v1/billing/subscriptions/:id/reactivate", o.reactivateSubscription,
		zip.WithOperationID("reactivateSubscription"),
		zip.WithSummary("Put a canceled subscription back on its plan"))
}

// Lists the plans the caller holds, with the count beside them.
//
// It is scoped to the caller's own org, so a query cannot widen it to another
// customer's. An org on nothing is an empty list, not a refusal — being on no
// plan is an answer.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) subscriptions(ctx context.Context, _ *noInput) (*plane.Subscriptions, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	in := plane.SubsIn{}
	if c, ok := cloud.Request(ctx); ok {
		in.UserID = strings.TrimSpace(c.Query("userId"))
		in.Status = strings.TrimSpace(c.Query("status"))
	}
	return ask(ctx, org, "subscriptions", func(ctx context.Context) (*plane.Subscriptions, error) {
		return commercepeer.BillingSubscriptions(ctx, &in)
	})
}

// Ends a subscription.
//
// It cancels at the END OF THE PAID PERIOD by default, because a customer who
// cancels has already paid for the period they are in and taking it away is
// taking money for nothing. `atPeriodEnd: false` ends it at once, which is the
// caller asking for that.
//
// A subscription from another org is not found rather than refused, so an id
// cannot be probed for existence.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cancelSubscription(ctx context.Context, in *plane.SubscriptionRef) (*plane.Subscription, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	ref := plane.SubscriptionRef{ID: pathID(ctx), AtPeriodEnd: true}
	if in != nil && bodyStatedPeriodEnd(ctx) {
		ref.AtPeriodEnd = in.AtPeriodEnd
	}
	return ask(ctx, org, "cancel subscription", func(ctx context.Context) (*plane.Subscription, error) {
		return commercepeer.BillingSubscriptionCancel(ctx, &ref)
	})
}

// Puts a canceled subscription back on its plan.
//
// What asks for this is usually a recovered payment method or a support tool
// rather than a browser, which is most of the argument for it having an address
// at all. The engine decides whether the move is legal; a row it will not
// reactivate comes back with its own reason.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) reactivateSubscription(ctx context.Context, _ *plane.SubscriptionRef) (*plane.Subscription, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	ref := plane.SubscriptionRef{ID: pathID(ctx)}
	return ask(ctx, org, "reactivate subscription", func(ctx context.Context) (*plane.Subscription, error) {
		return commercepeer.BillingSubscriptionReactivate(ctx, &ref)
	})
}

// pathID is the id in the URL, which is the authority for WHICH row is acted on.
// The body may carry one too — zip binds path last, so it already wins — and
// reading it here rather than off the input makes that explicit: a body id must
// never redirect a write.
func pathID(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.Param("id")
	}
	return ""
}

// bodyStatedPeriodEnd reports whether the caller actually said when to cancel.
//
// It matters because the DEFAULT is true and the zero value of a bool is false:
// without this, a cancel with no body at all would end the subscription
// immediately and take the rest of a paid period with it. An empty body means
// "the default", which is the kind way round.
func bodyStatedPeriodEnd(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	if !ok {
		return false
	}
	return strings.Contains(string(c.Body()), "atPeriodEnd")
}
