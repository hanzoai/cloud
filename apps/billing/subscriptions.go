package billing

// subscriptions.go serves the customer's own plan: what they are on, ending it,
// and putting it back.
//
// The rows are commerce's and the lifecycle is its engine's, so a move the
// engine will not make comes back as the caller's own refusal rather than being
// re-decided here. One state machine, in one place — an endpoint that re-checked it
// would be a second opinion about whether a paid period may be cancelled twice.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// mountSubscriptions registers the subscription family. Called from routes.
func mountSubscriptions(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Get("/subscriptions", o.subscriptions)
	g.Post("/subscriptions/:id/cancel", o.cancelSubscription,
		zip.WithOperationID("cancelSubscription"),
		zip.WithSummary("End a subscription"))
	g.Post("/subscriptions/:id/reactivate", o.reactivateSubscription,
		zip.WithOperationID("reactivateSubscription"),
		zip.WithSummary("Put a canceled subscription back on its plan"))
	g.Post("/subscriptions/:id/resize", o.resizeSubscription,
		zip.WithOperationID("resizeSubscription"),
		zip.WithSummary("Change a team's seats"))
	g.Put("/subscriptions/:id/seats", o.assignSeats,
		zip.WithOperationID("assignSeats"),
		zip.WithSummary("Assign a team's seats"))
}

// Lists the plans the caller may see, with the count beside them.
//
// An admin of the org, and the platform, see every plan the org holds, and may
// narrow to one wallet with `userId`. A member sees the plans of the wallet they
// pay from — the org's own for a member of a pooled org, their own in the shared
// signup org — and never another member's; the card a plan renews on is left off
// a row the member cannot change. A query cannot widen any of it to another
// customer's org. An org on nothing is an empty list, not a refusal — being on no
// plan is an answer.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) subscriptions(ctx context.Context, _ *cloud.Unit) (*client.Subscriptions, error) {
	// payer answers 401 off the HTTP path, as every finance read does; once it
	// resolves, the request it read is the one asked about below.
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	all := administers(c)
	own := ownWallet(c, org, subject)
	in := client.SubsIn{UserID: subject, Status: strings.TrimSpace(c.Query("status"))}
	if all {
		in.UserID = strings.TrimSpace(c.Query("userId"))
	} else if subject == "" {
		// A member whose wallet could not be named holds no plan; an empty filter
		// would be every row in the org.
		return &client.Subscriptions{Rows: []client.Subscription{}}, nil
	}
	if claim := strings.TrimSpace(c.Header("X-Subscription-Claim")); claim != "" && subject != "" && (all || own) {
		in.Claim = subject + "|" + claim
	}
	out, err := ask(ctx, org, "subscriptions", func(ctx context.Context) (*client.Subscriptions, error) {
		return commercepeer.BillingSubscriptions(ctx, &in)
	})
	if err != nil {
		return nil, err
	}
	if !all && !own {
		for i := range out.Rows {
			out.Rows[i].DefaultPaymentMethod = ""
		}
	}
	return out, nil
}

// subscriptionAct is what a caller says about a subscription it acts on: which
// one (the path) and, for a cancel, when. Who may act is not the caller's to say.
type subscriptionAct struct {
	// ID is the subscription, from the path.
	ID string `json:"id"`
	// AtPeriodEnd cancels at the end of the paid period rather than at once. It
	// defaults TRUE, because a customer who cancels has already paid for the
	// period they are in.
	AtPeriodEnd bool `json:"atPeriodEnd,omitempty"`
}

// Ends a subscription.
//
// It cancels at the END OF THE PAID PERIOD by default, because a customer who
// cancels has already paid for the period they are in and taking it away is
// taking money for nothing. `atPeriodEnd: false` ends it at once, which is the
// caller asking for that.
//
// The org's plan is its admins' to end, and the platform's in support mode. A
// member is refused with 403 and told to ask an admin; a member billed from a
// wallet of their own may end that wallet's plan. Every one is on the audit
// trail, the operator named when it is the platform acting inside the org.
//
// A subscription from another org, or held by a wallet the caller may not
// change, is not found rather than refused, so an id cannot be probed for
// existence.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cancelSubscription(ctx context.Context, in *subscriptionAct) (*client.Subscription, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, cancelAct)
	if err != nil {
		return nil, err
	}
	ref := client.SubscriptionRef{ID: pathID(ctx), AtPeriodEnd: true, Subject: s.subject, Privileged: s.all}
	if in != nil && bodyStatedPeriodEnd(ctx) {
		ref.AtPeriodEnd = in.AtPeriodEnd
	}
	return ask(ctx, s.org, "cancel subscription", func(ctx context.Context) (*client.Subscription, error) {
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
// Resuming the org's plan commits its card to the next renewal, so it takes the
// authority ending it does: an admin of the org or the platform, or a member for
// a wallet of their own. A member is refused with 403 and told to ask an admin.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) reactivateSubscription(ctx context.Context, _ *subscriptionAct) (*client.Subscription, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, resumeAct)
	if err != nil {
		return nil, err
	}
	ref := client.SubscriptionRef{ID: pathID(ctx), Subject: s.subject, Privileged: s.all}
	return ask(ctx, s.org, "reactivate subscription", func(ctx context.Context) (*client.Subscription, error) {
		return commercepeer.BillingSubscriptionReactivate(ctx, &ref)
	})
}

// resize is the seats a team opens its next period at, from the body.
type resize struct {
	// Quantity is the seats of the subscription's own seat type; 0 keeps them.
	Quantity int `json:"quantity,omitempty"`
	// Lines are the team's other seat types and the seats of each:
	// [{"planId":"team-premium","quantity":2}]. A seat type of the team the
	// subscription does not hold yet is added.
	Lines []client.SaleLine `json:"lines,omitempty"`
}

// Changes a team's seats: how many standard and premium seats its next period
// opens at.
//
// `quantity` is the subscription's own seat type, 0 to keep it, and `lines` each
// other seat type: `{"lines":[{"planId":"team-premium","quantity":1}]}` adds a
// premium seat to a team bought with standard ones. The renewal that opens the
// next period charges the seats asked for, and the period in hand keeps the ones
// it was paid for, so nothing is charged now. A team is at least 2 seats and at
// most 100 across every seat type; past either, or naming a plan that is not a
// seat type of the team, is 400 and nothing changes.
//
// The team's seats are its admins' to change, and the platform's. A member is
// refused with 403 and told to ask an admin. A subscription from another org, or
// held by a wallet the caller may not change, is not found.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) resizeSubscription(ctx context.Context, in *resize) (*client.Subscription, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, resizeAct)
	if err != nil {
		return nil, err
	}
	ref := client.ResizeIn{ID: pathID(ctx), Subject: s.subject, Privileged: s.all}
	if in != nil {
		ref.Quantity, ref.Lines = in.Quantity, in.Lines
	}
	return ask(ctx, s.org, "change seats", func(ctx context.Context) (*client.Subscription, error) {
		return commercepeer.BillingSubscriptionResize(ctx, &ref)
	})
}

// assignment is which member holds which of a team's seat types, from the body.
type assignment struct {
	// Assigned is every seat assigned: the member, and the seat type (a plan the
	// subscription holds seats of, "team" or "team-premium") they hold. It
	// replaces what was assigned; an empty list unassigns every seat.
	Assigned []client.SubscriptionSeat `json:"assigned"`
}

// Assigns a team's seats: which member holds a standard seat and which a premium
// one, replacing what was assigned.
//
// A seat type is given no more members than the seats of it paid for — the fewer
// of the period in hand's and the next one's — and a member holds one seat; past
// either the request is 400 and nothing changes. Which member holds a seat moves
// no money and bounds no usage: a team's usage is all of its seats' together.
//
// The team's seats are its admins' to assign, and the platform's. A member is
// refused with 403 and told to ask an admin. A subscription from another org, or
// held by a wallet the caller may not change, is not found.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) assignSeats(ctx context.Context, in *assignment) (*client.Subscription, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, seatsAct)
	if err != nil {
		return nil, err
	}
	ref := client.SeatsIn{ID: pathID(ctx), Subject: s.subject, Privileged: s.all}
	if in != nil {
		ref.Assigned = in.Assigned
	}
	return ask(ctx, s.org, "assign seats", func(ctx context.Context) (*client.Subscription, error) {
		return commercepeer.BillingSubscriptionSeats(ctx, &ref)
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
