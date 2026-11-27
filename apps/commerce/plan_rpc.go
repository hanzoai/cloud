// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// plan_rpc.go — what a subject is on and what is left of it, over the internal
// plane: the tier their subscriptions confer, the month measured against the
// plan, and the subscription rows themselves.
//
// THERE IS NO TIER OVERRIDE. A tier is derived from recorded subscriptions, for
// every org: hanzo, admin, lux and zoo resolve like customers, and no header, query
// or role names one. They are the subject's own, with one addition: an org that holds
// no live paid plan of its own and has a plan link reads the plan of the account the
// link names, derived the same way for that account's own subject (planlink.go). A
// link says whose subscriptions to read, never which tier to serve, and the lent plan
// is never the borrowing org's to cancel.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/account"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/billinginvoice"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/models/paymentmethod"
	commercesubscription "github.com/hanzoai/commerce/models/subscription"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	commercetypes "github.com/hanzoai/commerce/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk"
)

// exposePlan publishes the tier, rollup and subscription ops. Mount calls it.
func exposePlan() {
	cloud.Plane().Post("/billing/tier", planeTier,
		zip.WithOperationID(client.BillingTier),
		zip.WithSummary("What a subject's plan allows and what they can spend"))
	cloud.Plane().Post("/billing/rollup", planeRollup,
		zip.WithOperationID(client.BillingRollup),
		zip.WithSummary("A subject's month against their plan, and the wallet beside it"))
	cloud.Plane().Post("/billing/subscriptions", planeSubscriptions,
		zip.WithOperationID(client.BillingSubscriptions),
		zip.WithSummary("The plans a subject holds"))
	cloud.Plane().Post("/billing/fleet/subscriptions", planeFleetSubscriptions,
		zip.WithOperationID(client.BillingFleetSubscriptions),
		zip.WithSummary("Every org's subscriptions, for the platform's SuperAdmin"))
	cloud.Plane().Post("/billing/subscription/cancel", planeSubscriptionCancel,
		zip.WithOperationID(client.BillingSubscriptionCancel),
		zip.WithSummary("End a subscription"))
	cloud.Plane().Post("/billing/subscription/reactivate", planeSubscriptionReactivate,
		zip.WithOperationID(client.BillingSubscriptionReactivate),
		zip.WithSummary("Put a canceled subscription back on its plan"))
	cloud.Plane().Post("/billing/subscription/resize", planeSubscriptionResize,
		zip.WithOperationID(client.BillingSubscriptionResize),
		zip.WithSummary("Change a team's seats"))
	cloud.Plane().Post("/billing/subscription/seats", planeSubscriptionSeats,
		zip.WithOperationID(client.BillingSubscriptionSeats),
		zip.WithSummary("Assign a team's seats"))
	cloud.Plane().Post("/billing/subscription/record", planeSubscriptionRecord,
		zip.WithOperationID(client.BillingSubscriptionRecord),
		zip.WithSummary("Record a plan paid outside checkout"))
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
	// A store hiccup is returned rather than answered as Free — see above.
	view, verr := commercebilling.ReadTier(ctx, org, in.Subject)
	if verr != nil {
		return nil, zip.Errorf(500, "failed to resolve tier")
	}
	if onPlan(view) {
		return tierAnswer(view), nil // an org's own plan always wins over a link
	}
	lent, from, ok, err := borrowedFrom(ctx, org.Name)
	if err != nil {
		return nil, linkFault(err)
	}
	if !ok {
		return tierAnswer(view), nil
	}
	// The lent plan, the borrowing org's own money: nothing crosses ledgers, and the
	// subscription id stays empty because the row is not this org's to cancel.
	return &client.Tier{
		User:       view.User,
		Plan:       lent.Plan,
		Tier:       tierLimits(lent),
		Balance:    tierBalance(view),
		PlanName:   lent.PlanName,
		PlanHolder: from.String(),
	}, nil
}

// linkFault answers a plan link that could not be read: a 5xx the store already chose,
// else the same "failed to resolve tier" an unreadable ledger gets — never Free.
func linkFault(err error) error {
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		return he
	}
	return zip.Errorf(500, "failed to resolve tier")
}

// tierAnswer moves a tier onto the wire, the row that serves it named.
func tierAnswer(view *commercebilling.TierView) *client.Tier {
	return &client.Tier{
		User:         view.User,
		Plan:         view.Plan,
		Subscription: view.Subscription,
		Tier:         tierLimits(view),
		Balance:      tierBalance(view),
		PlanName:     view.PlanName,
	}
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
	if in.Claim != "" {
		claimGuestSubscription(ctx, org, in.Claim)
	}
	out, err := subscriptionsOf(ctx, org, in.UserID, in.Status)
	if err != nil {
		return nil, zip.Errorf(500, "failed to list subscriptions")
	}
	return &client.Subscriptions{Rows: out, Count: len(out)}, nil
}

// claimGuestSubscription links an unclaimed guest subscription (bought before sign-in
// under hanzo/guest-*) to the authenticated buyer's wallet when they present the exact
// (subscriptionId, invoiceId) receipt pair from the sale.
func claimGuestSubscription(ctx context.Context, org *organization.Organization, raw string) {
	targetSubject, token, ok := strings.Cut(strings.TrimSpace(raw), "|")
	if !ok || strings.TrimSpace(targetSubject) == "" {
		return
	}
	subID, invID, ok := strings.Cut(strings.TrimSpace(token), ":")
	if !ok || strings.TrimSpace(subID) == "" || strings.TrimSpace(invID) == "" {
		return
	}
	guestOrg := org
	if !strings.EqualFold(org.Name, account.SignupOrg) {
		resolved, err := commerceorg.Resolve(ctx, account.SignupOrg)
		if err != nil {
			return
		}
		guestOrg = resolved
	}
	guestDB := datastore.New(guestOrg.Namespaced(ctx))
	sub := commercesubscription.New(guestDB)
	if err := sub.GetById(subID); err != nil {
		return
	}
	guestSubject := sub.UserId
	if !strings.HasPrefix(guestSubject, account.SignupOrg+"/guest-") {
		return
	}
	inv := billinginvoice.New(guestDB)
	if err := inv.GetById(invID); err != nil {
		return
	}
	if inv.SubscriptionId != sub.Id() || inv.UserId != guestSubject {
		return
	}
	var pm *paymentmethod.PaymentMethod
	pmID := strings.TrimSpace(sub.DefaultPaymentMethod)
	if pmID != "" {
		candidate := paymentmethod.New(guestDB)
		if err := candidate.GetById(pmID); err == nil && (candidate.UserId == guestSubject || candidate.CustomerId == guestSubject) {
			pm = candidate
		}
	}
	if strings.EqualFold(guestOrg.Name, org.Name) {
		sub.UserId = targetSubject
		_ = sub.Update()
		inv.UserId = targetSubject
		_ = inv.Update()
		if pm != nil {
			pm.UserId = targetSubject
			pm.CustomerId = targetSubject
			_ = pm.Update()
		}
		return
	}
	targetDB := datastore.New(org.Namespaced(ctx))
	_ = sub.Delete()
	_ = inv.Delete()
	if pm != nil {
		_ = pm.Delete()
	}
	sub.Rebind(targetDB)
	sub.UserId = targetSubject
	_ = sub.SetKey(subID)
	_ = sub.Put()

	inv.Rebind(targetDB)
	inv.UserId = targetSubject
	_ = inv.SetKey(invID)
	_ = inv.Put()

	if pm != nil {
		pm.Rebind(targetDB)
		pm.UserId = targetSubject
		pm.CustomerId = targetSubject
		_ = pm.SetKey(pmID)
		_ = pm.Put()
	}
}

// subscriptionsOf is one org's subscriptions as the plane carries them: commerce's
// view of each row, with what the sale wrote on it and when it was last paid. The
// org's own read and the operator's fleet read (planeFleetSubscriptions) both answer
// through it, so a row reads the same on either. An org that holds no subscription —
// most of the fleet — costs no invoice read.
//
// When a subscription's invoices do not read, the row is answered without them: the
// AI gate and the billing page read plans through here, and a date that only helps a
// reader must not take the plan from them.
func subscriptionsOf(ctx context.Context, org *organization.Organization, userID, status string) ([]client.Subscription, error) {
	rows, err := commercebilling.Subscriptions(ctx, org, userID, status)
	if err != nil {
		return nil, err
	}
	sales, err := soldOf(ctx, org, userID, status)
	if err != nil {
		return nil, err
	}
	out := make([]client.Subscription, 0, len(rows))
	for i := range rows {
		row := subscriptionRow(&rows[i])
		sale := sales[row.ID]
		row.Test, row.Coupon = sale.test, sale.coupon
		paid, err := invoicePaid(ctx, org, row.ID)
		if err != nil {
			luxlog.Default().Warn("commerce: a subscription's invoices did not read; its row says no invoice payment",
				"org", org.Name, "subscription", row.ID, "err", err)
		}
		if sale.recorded.After(paid) {
			paid = sale.recorded
		}
		if !paid.IsZero() {
			row.LastPaid = stamp(paid)
		}
		out = append(out, row)
	}
	return out, nil
}

// lastInvoices is how many of a subscription's newest paid invoices are read for its
// latest payment: an older invoice can be paid after a newer one, so the latest
// payment is the latest paid time among the newest few, not the newest invoice's.
const lastInvoices = 10

// paidInvoicesOf lists a subscription's invoices: commerce's own read, a variable so a
// test can make it fail.
var paidInvoicesOf = commercebilling.ListInvoices

// invoicePaid is when a subscription's latest paid invoice was paid; zero when none
// was, or when they did not read.
func invoicePaid(ctx context.Context, org *organization.Organization, sub string) (time.Time, error) {
	invs, err := paidInvoicesOf(ctx, org, "", "paid", sub, lastInvoices, 0)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, inv := range invs {
		if inv.PaidAt != nil && inv.PaidAt.After(latest) {
			latest = *inv.PaidAt
		}
	}
	return latest, nil
}

// sold is what the sale wrote on a subscription row that commerce's view does
// not carry: the sandbox flag, the coupon its first period was bought with, and the
// start of the latest period recorded as paid outside a checkout.
type sold struct {
	test     bool
	coupon   string
	recorded time.Time
}

// soldOf reads those facts per subscription id from the same query the view
// was built from, filtered the same way. Only rows carrying one are present.
func soldOf(ctx context.Context, org *organization.Organization, userID, status string) (map[string]sold, error) {
	subs, err := commercebilling.ListSubscriptions(ctx, org, userID, status)
	if err != nil {
		return nil, err
	}
	out := map[string]sold{}
	for _, s := range subs {
		if s == nil {
			continue
		}
		code, _ := s.Metadata["coupon"].(string)
		recorded := recordedStart(s.Metadata)
		if s.Test || code != "" || !recorded.IsZero() {
			out[s.Id()] = sold{test: s.Test, coupon: code, recorded: recorded}
		}
	}
	return out, nil
}

// recordedStart is the start of the latest period recorded on a row as paid outside
// a checkout — "periods" in its metadata (api/billing recordMetadata). A record keeps
// no time of its own, so the start of the period it paid is the nearest the books
// come to when that payment arrived. Zero when no period was recorded.
func recordedStart(meta map[string]any) time.Time {
	periods, _ := meta["periods"].([]any)
	var latest time.Time
	for _, p := range periods {
		m, _ := p.(map[string]any)
		start, _ := m["start"].(string)
		if t, err := time.Parse(time.RFC3339, start); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// Ends a subscription — at the end of the period already paid for, or at once.
//
// The lifecycle belongs to the engine, so a move it will not make comes back as
// the caller's own refusal rather than being re-decided here: one state machine,
// in one place. A subscription from another org, or held by another wallet than
// the caller may change, is not found rather than refused, so an id cannot be
// probed for existence.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionCancel(ctx context.Context, in *client.SubscriptionRef) (*client.Subscription, error) {
	org, err := orgOf(ctx, "cancel subscription")
	if err != nil {
		return nil, err
	}
	sub, serr := commercebilling.CancelSubscription(ctx, org, holderOf(in), in.ID, in.AtPeriodEnd)
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to cancel subscription")
	}
	canceled(org.Name, sub)
	mirrorOrgSubscriptions(org)
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
	sub, serr := commercebilling.ReactivateSubscription(ctx, org, holderOf(in), in.ID)
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to reactivate subscription")
	}
	told(org.Name, funds.Plan)
	mirrorOrgSubscriptions(org)
	mirrorOrgInvoices(org)
	row := subscriptionRow(sub)
	return &row, nil
}

// Sets which member holds which of a team's seat types, replacing what was
// assigned, and answers with the subscription as it now stands.
//
// No seat type is given more members than the seats of it paid for, a member
// holds one seat, and a seat type the subscription holds no seats of is refused.
// Which member holds a seat moves no money and bounds no usage: a team's usage is
// all of its seats' together. A subscription held by a wallet the caller may not
// change is not found, as for a cancel.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionSeats(ctx context.Context, in *client.SeatsIn) (*client.Subscription, error) {
	org, err := orgOf(ctx, "assign seats")
	if err != nil {
		return nil, err
	}
	seats := make([]commercesubscription.Seat, 0, len(in.Assigned))
	for _, a := range in.Assigned {
		seats = append(seats, commercesubscription.Seat{Member: a.Member, Plan: a.Plan})
	}
	holder := in.Subject
	if in.Privileged {
		holder = commercebilling.AnyHolder
	}
	sub, serr := commercebilling.AssignSeats(ctx, org, holder, in.ID, seats)
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to assign seats")
	}
	mirrorOrgSubscriptions(org)
	row := subscriptionRow(sub)
	return &row, nil
}

// Sets the seats a team opens its next period at, and answers with the
// subscription as it now stands.
//
// Quantity is the plan's own seat type (0 keeps it) and each line another seat
// type of the team's group, one the subscription does not hold yet among them:
// a team bought with standard seats adds premium ones here. The renewal that
// opens the next period charges them, and the period in hand keeps the seats it
// was paid for. The team is at least 2 seats and at most 100, counted over every
// seat type. A subscription held by a wallet the caller may not change is not
// found, as for a cancel.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionResize(ctx context.Context, in *client.ResizeIn) (*client.Subscription, error) {
	org, err := orgOf(ctx, "change seats")
	if err != nil {
		return nil, err
	}
	holder := in.Subject
	if in.Privileged {
		holder = commercebilling.AnyHolder
	}
	sub, serr := commercebilling.ChangeSeats(ctx, org, holder, in.ID, in.Quantity, saleLines(in.Lines))
	if serr != nil {
		return nil, subscriptionFault(serr, "failed to change seats")
	}
	mirrorOrgSubscriptions(org)
	row := subscriptionRow(sub)
	return &row, nil
}

// holderOf is whose subscriptions a ref may change: every one in the org for a
// caller the endpoint found Privileged, the wallet it pays from otherwise. An
// empty subject holds nothing, so a ref that states no authority reaches no row
// — answered not found, like a row another wallet holds.
func holderOf(in *client.SubscriptionRef) string {
	if in.Privileged {
		return commercebilling.AnyHolder
	}
	return in.Subject
}

// Records a plan the customer paid for outside checkout — an invoice raised in
// the processor's own dashboard, a wire — and opens it on the period that
// payment covers, or moves the account's external plan on to it.
//
// With replaces naming the plan the account holds, it takes that plan over: the
// new one is recorded and paid first, and the held one ends at once only after,
// so a refusal leaves the account on the plan it held.
//
// It charges nothing, now or later: the row is collected externally, so the
// engine never answers due for it and no cycle invoices or collects it; with no
// later payment recorded it lapses at its period's end. It grants no credit. The
// stated payment is checked against the plan's catalog price, or the customer's
// own price when staff agreed one, so a payment recorded against the wrong plan
// is refused, not booked.
//
// The processor is one of client.RecordProcessors. Prepaid money is none: a plan
// is never paid from the balance or credit, and "balance" is refused (400
// plan_needs_card).
//
// SuperAdmin only, re-checked here against the principal that asked, because
// the op opens a paid tier without a sale. The org is the caller's, delegated by
// the operator's edge; nothing in the input can name another tenant's books.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionRecord(ctx context.Context, in *client.RecordPlanIn) (*client.RecordedPlan, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "record subscription")
	if err != nil {
		return nil, err
	}
	start, serr := time.Parse(time.RFC3339, in.PeriodStart)
	end, eerr := time.Parse(time.RFC3339, in.PeriodEnd)
	if serr != nil || eerr != nil {
		return nil, zip.ErrBadRequest("periodStart and periodEnd must be RFC 3339 times")
	}
	rec, rerr := commercebilling.RecordSubscription(ctx, org, commercebilling.RecordIn{
		Subject:     in.Subject,
		PlanID:      in.PlanID,
		Interval:    commercetypes.Interval(in.Interval),
		Quantity:    in.Quantity,
		PriceCents:  in.PriceCents,
		PeriodStart: start,
		PeriodEnd:   end,
		Processor:   in.Processor,
		Reference: map[string]string{
			"invoice":  in.Reference.Invoice,
			"order":    in.Reference.Order,
			"payment":  in.Reference.Payment,
			"customer": in.Reference.Customer,
			"receipt":  in.Reference.Receipt,
		},
		Terms:    in.Terms,
		Replaces: in.Replaces,
		Lines:    saleLines(in.Lines),
		Events:   eventsFrom(ctx),
	})
	if rerr != nil {
		return nil, saleFault(rerr)
	}
	told(org.Name, funds.Plan)
	mirrorOrgSubscriptions(org)
	mirrorOrgInvoices(org)
	out := &client.RecordedPlan{Outcome: rec.Outcome, Subscription: subscriptionRow(&rec.Subscription)}
	if rec.Replaced != nil {
		replaced := subscriptionRow(rec.Replaced)
		out.Replaced = &replaced
	}
	return out, nil
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
	row := client.Subscription{
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
		Settled:              s.Settled,
		Seats:                s.Seats,
		ChargedCents:         s.ChargedCents,
		Held:                 s.Held,
		CustomerCents:        s.CustomerCents,
		Plan: client.SubscriptionPlan{
			ID:       s.Plan.ID,
			Slug:     s.Plan.Slug,
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
	for _, l := range s.Lines {
		row.Lines = append(row.Lines, client.SubscriptionLine{Plan: l.Plan, Name: l.Name, Price: l.Price,
			Quantity: l.Quantity, PendingQuantity: l.PendingQuantity, Seats: l.Seats})
	}
	for _, a := range s.Assigned {
		row.Assigned = append(row.Assigned, client.SubscriptionSeat{Member: a.Member, Plan: a.Plan})
	}
	return row
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
