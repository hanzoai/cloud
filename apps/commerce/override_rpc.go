// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"strings"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercetypes "github.com/hanzoai/commerce/types"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeOverrides publishes what staff change on a customer's plan by hand beside
// recording a payment: moving a period's end, and a customer's own price. Mount
// calls it. Every one is the platform's act, re-checked here.
func exposeOverrides() {
	cloud.Plane().Post("/billing/subscription/period", planeSubscriptionPeriod,
		zip.WithOperationID(client.BillingSubscriptionPeriod),
		zip.WithSummary("Move the end of a subscription's period"))
	cloud.Plane().Post("/billing/customer/price", planeCustomerPrice,
		zip.WithOperationID(client.BillingCustomerPrice),
		zip.WithSummary("Set what one customer pays for a plan, every period"))
	cloud.Plane().Post("/billing/customer/prices", planeCustomerPrices,
		zip.WithOperationID(client.BillingCustomerPrices),
		zip.WithSummary("Every price agreed with one org's customers"))
}

// Moves the end of the period a subscription holds — or, for a card plan
// scheduled to start, its start — to a time in the future.
//
// An extension is time given: a card plan renews that much later, a plan
// collected outside commerce is covered that much longer. A card plan scheduled
// to start at the old end starts at the new one, so the two never overlap. No
// money moves and no invoice is raised; each move is kept on the row. Ending a
// plan now is a cancel, not this.
//
// SuperAdmin only, re-checked here; the org is the caller's, delegated by the
// operator's edge.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionPeriod(ctx context.Context, in *client.PeriodIn) (*client.Adjusted, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "adjust period")
	if err != nil {
		return nil, err
	}
	end, perr := time.Parse(time.RFC3339, strings.TrimSpace(in.End))
	if perr != nil {
		return nil, zip.ErrBadRequest("end must be an RFC 3339 time")
	}
	adj, aerr := commercebilling.AdjustPeriod(ctx, org, commercebilling.AdjustIn{ID: in.ID, End: end, Note: in.Note})
	if aerr != nil {
		return nil, saleFault(aerr)
	}
	out := &client.Adjusted{Subscription: subscriptionRow(&adj.Subscription), From: stamp(adj.From)}
	if adj.Moved != nil {
		moved := subscriptionRow(adj.Moved)
		out.Moved = &moved
	}
	return out, nil
}

// Sets what one customer pays for one seat of a plan each period, below the
// plan's list price, or removes it with 0.
//
// A card sale of the plan to the customer charges it, the subscription it opens
// carries it, and every renewal bills it: the invoice shows the plan at list and
// the difference as the customer's discount. It is put on every live
// subscription of the plan the customer holds, and a private plan is sold by card
// only to a customer with a price of it. No promotion applies beside it.
//
// SuperAdmin only, re-checked here; the org is the caller's, delegated by the
// operator's edge.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCustomerPrice(ctx context.Context, in *client.CustomerPriceIn) (*client.PriceSet, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "customer price")
	if err != nil {
		return nil, err
	}
	set, serr := commercebilling.SetCustomerPrice(ctx, org, commercebilling.PriceIn{
		Subject: in.Subject, Plan: in.Plan, Interval: commercetypes.Interval(strings.TrimSpace(in.Interval)),
		Cents: in.Cents, Note: in.Note,
	})
	if serr != nil {
		return nil, saleFault(serr)
	}
	out := &client.PriceSet{Price: client.CustomerPrice(set.Price), Subscriptions: make([]client.Subscription, 0, len(set.Subscriptions))}
	for i := range set.Subscriptions {
		out.Subscriptions = append(out.Subscriptions, subscriptionRow(&set.Subscriptions[i]))
	}
	return out, nil
}

// Answers every price staff agreed with the org's customers, each beside the
// plan's list price for the same period.
//
// SuperAdmin only, re-checked here.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCustomerPrices(ctx context.Context, _ *cloud.Unit) (*client.CustomerPrices, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "customer prices")
	if err != nil {
		return nil, err
	}
	rows, rerr := commercebilling.CustomerPrices(ctx, org)
	if rerr != nil {
		return nil, zip.Errorf(500, "customer prices could not be read")
	}
	out := &client.CustomerPrices{Rows: make([]client.CustomerPrice, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, client.CustomerPrice(r))
	}
	return out, nil
}
