// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// emit.go — A SETTLED CARD PAYMENT IS AN ORDER, STATED ON THE EVENT PLANE.
//
// # The sale nobody outside this process could see
//
// A card cleared, [screen.settle] funded the wallet and [screen.learn] taught the
// risk model, and that was the end of it. The organisation's own lenses
// (/v1/event/insights, the commerce overview that counts `order_completed` and sums its
// revenue) saw nothing, and neither did apps/destinations — the fan-out that
// forwards a conversion to whichever ad platforms the org has connected. So the
// ONLY Purchase those platforms ever received was the browser pixel's: fired from
// a page, blocked by every content blocker, lost on every navigation away, and
// carrying whatever the page said the order was worth. The authoritative purchase
// — the one where money actually moved, sized by the amount the card was charged —
// was the one nobody forwarded.
//
// This states it, at the point both mints already agree a charge cleared.
//
// # It is the event plane's endpoint, not a second one
//
// The call is [eventpeer.EventCapture] over the peer socket, which is the
// SAME write core POST /v1/event reaches (apps/event/event_rpc.go). One
// admission, one normalizer, one storage projection, one fan-out — so a purchase
// this process states and a purchase a browser posts are the same kind of thing,
// counted once by the same lens and translated once by the same translator
// (apps/destination/translate.go maps `order_completed` onto the normalized
// Purchase every adapter renders). A private path from commerce to the ad
// platforms would be a second answer to "what is a sale", and the two would
// disagree the first time either changed.
//
// apps/risk/emit.go states its decisions the same way for the same reasons; this
// is that shape, applied to money.
//
// # The payer is a customer
//
// A live payment also makes its payer a customer of the org it was paid in, in that
// org's CRM ([customer]): the Customers view lists the people who pay. It reads and
// moves no money, writes no cap, and touches no other org. It runs beside the
// statement, so it is detached, bounded and dropped with it; the payer's next payment
// files them. A sweep's charge has no address, so [customers] reads IAM's, and a wire
// carries the address its invoice was issued to (customer.go).
//
// # Detached, bounded, dropped under pressure
//
// The money has moved and the customer has been answered by the time this runs, so
// it is [screen.learn]'s posture exactly: its own budget, a ceiling on how many may
// be in flight, a drop rather than a queue past it, panic-guarded, and every error
// logged and discarded. A conversion row is expendable; a settled payment is not.

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercecurrency "github.com/hanzoai/commerce/models/types/currency"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/network"
)

// orderCompleted is the name the sale is filed under, and it is a CONSTANT because
// `name` is the column every lens groups by. It is the canonical events vocabulary's
// own word for a completed sale — what /v1/event/insights counts as an order and what
// apps/destinations translates into each platform's Purchase.
const orderCompleted = "order_completed"

// emitBudget bounds ONE statement end to end, including waking a cold analytics
// child (plane.Call single-flights the start). It is generous for [teachBudget]'s
// reason: it bounds a DETACHED goroutine and not a request, so nothing here is on
// the customer's critical path.
const emitBudget = 5 * time.Second

// maxEmits is how many statements may be in flight at once. Past it one is DROPPED
// rather than queued, for [maxTeaching]'s reason: a queue defers the loss instead of
// bounding it, and the memory this process holds must not be a function of how fast
// money is arriving.
const maxEmits = 32

// emitting is that ceiling, as a counting semaphore. A send that cannot proceed is an
// answer — there is no room — and never a place to block a settled payment.
var emitting = make(chan struct{}, maxEmits)

// send is the ONE plane call, held in a variable for the one thing a variable buys
// here: a test can observe exactly what LEAVES this process without standing up an
// analytics child to receive it. It is never reassigned in production — the only
// writer is a test, and the compiler holds the signature to the generated client's.
var send = eventpeer.EventCapture

// file is the CRM's customer write, held in a variable for send's reason.
var file = crm.Customer

// emit states a settled payment as a completed order on the calling organisation's
// own event plane.
func (s screen) emit(p payment, ref string) {
	// ONLY REAL MONEY IS A SALE. A sandbox charge costs a test card nothing, and
	// stated as a sale it would reach the platform's ad accounts as revenue
	// anyone could mint. The receipt says which books it was; a charge whose
	// books nobody said, and every charge off mainnet, states nothing.
	//
	// A test-mode PLAN SALE is stated too, as test traffic: it carries `test` and no
	// amount, so the platforms file it under their test channels (GA4 debug_mode, Meta
	// Test Events) and no lens or ad account counts it as revenue. It is how the
	// checkout is proved end to end without moving money. A test-mode top-up buys no
	// plan and states nothing.
	live := p.moded && !p.test && !network.SandboxOnly()
	test := p.moded && p.plan != "" && (p.test || network.SandboxOnly())
	if !live && !test {
		s.lg.Debug("a settled payment was not stated: not known to be live money", "org", p.ledger)
		return
	}
	p.test = test
	in := purchase(p, ref)
	if in == nil {
		return
	}
	if live {
		go pulsed(p.ledger, ref, p.cents, p.currency)
	}
	// THE LEDGER IS THE TENANT, matching [screen.learn] exactly: a sale filed under a
	// different org from the one that connected the destinations is a conversion no
	// platform is ever sent.
	lg, org := s.lg, p.ledger
	select {
	case emitting <- struct{}{}:
	default:
		lg.Debug("a settled payment was not stated on the event plane: statements in flight are at the ceiling",
			"ceiling", maxEmits)
		return
	}
	// The peer call is read HERE, on the request's own goroutine, so the detached
	// goroutine below holds a VALUE rather than reading a package variable while
	// something else writes it — [screen.learn]'s reason, and a client read from a
	// goroutine nobody joins is a client no test can put back.
	call, put := send, file
	go func() {
		defer func() { <-emitting }()
		defer func() {
			if r := recover(); r != nil {
				lg.Error("stating a settled payment on the event plane panicked", "err", r)
			}
		}()
		state(context.Background(), lg, call, org, in)
		if live {
			filed(lg, org, customer(context.Background(), put, org, p))
		}
	}()
}

// customer files the payer of a settled live payment as a customer of the org it was
// paid in, in that org's CRM (apps/crm Customer), on a context that states that org: a
// request-derived one would act in the caller's own. Nothing is filed for a payer with
// no address or no commerce user, and an org with no CRM answers an error; money is
// neither read nor moved.
func customer(ctx context.Context, put func(context.Context, string, crm.Buyer, time.Time) error, org string, p payment) error {
	if p.email == "" || p.subject == "" {
		return nil
	}
	ask, cancel := context.WithTimeout(cloud.For(ctx, org), emitBudget)
	defer cancel()
	return put(ask, org, crm.Buyer{Email: p.email, User: p.subject}, time.Now())
}

// filed says why a payer was not filed, at the level of every other thing a payment's
// telemetry cannot do.
func filed(lg luxlog.Logger, org string, err error) {
	if err != nil {
		lg.Debug("a payer was not filed as a customer", "org", org, "err", err)
	}
}

// pulsed puts a cleared live payment on the SuperAdmin feed. Best effort, like the
// statement above: the money has moved whatever the bus answers.
func pulsed(org, ref string, cents int64, currency string) {
	_ = bus.Announce(bus.Pulse{
		Kind: "paid", Key: ref, Org: org, Cents: cents,
		Title:  "Invoice paid",
		Detail: fmt.Sprintf("%.2f %s", float64(cents)/100, strings.ToUpper(cmp.Or(currency, "usd"))),
	})
}

// state files one sale on org's plane and says nothing back: the money has
// moved whatever the plane answers. ctx bounds it along with [emitBudget].
func state(ctx context.Context, lg luxlog.Logger, call func(context.Context, *client.EventIn) (*client.EventCaptured, error), org string, in *client.EventIn) {
	// THE TENANT IS STATED, for [screen.learn]'s reason: cloud.For on a
	// request-derived context prefers the gateway's assertion and returns before it
	// reads a stated one, and the org that PAYS is not always the org that asked.
	ask, cancel := context.WithTimeout(cloud.For(ctx, org), emitBudget)
	defer cancel()
	out, err := call(ask, in)
	switch {
	case err != nil:
		// Every failure is the same fact and none of them is the payment's: an absent
		// peer, a refusing one and a timed-out one all mean the sale was not filed.
		lg.Debug("a settled payment was not stated on the event plane", "org", org, "err", err)
	case out != nil && out.Accepted == 0:
		lg.Debug("a settled payment was stated on the event plane and landed nowhere", "org", org)
	}
}

// sweeps is the screen the renewal and auto-reload sweeps state their sales
// through, set where the ops are exposed. A sweep is a schedule with no request,
// so it states each charge in line rather than detached — a detached statement
// past [maxEmits] would be dropped — inside [sweepBudget] for the whole sweep, so
// a wedged event plane costs the schedule a minute and never its timeout.
var sweeps screen

// sweepBudget bounds every statement one sweep makes.
const sweepBudget = time.Minute

// renewed states each paid live renewal as a completed order, keyed on its
// invoice: a renewal is a sale the platform made with nobody present. Its amount
// is the card's part alone (commerce Renewal.AmountCents), so a period paid from
// credits or a balance states nothing.
func renewed(rows []commercebilling.Renewal) {
	if network.SandboxOnly() {
		return // off mainnet no money is real, whatever a row's mode says
	}
	ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
	defer cancel()
	var payers []payment
	for _, r := range rows {
		if !r.Success || r.Test || r.InvoiceId == "" || r.AmountCents <= 0 || ctx.Err() != nil {
			continue
		}
		p := payment{ledger: r.OrgName, subject: r.UserId, cents: r.AmountCents, currency: r.Currency, recurring: true}
		if in := purchase(p, r.InvoiceId); in != nil {
			go pulsed(r.OrgName, r.InvoiceId, r.AmountCents, r.Currency)
			state(ctx, sweeps.lg, send, r.OrgName, in)
			payers = append(payers, p)
		}
	}
	customers(sweeps.lg, file, payers)
}

// reloaded states each auto-reload the sweep charged, keyed on its transaction.
func reloaded(rows []commercebilling.RechargeResult) {
	if network.SandboxOnly() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
	defer cancel()
	var payers []payment
	for _, r := range rows {
		if !r.Charged || r.Test || r.TransactionId == "" || ctx.Err() != nil {
			continue
		}
		p := payment{ledger: r.OrgName, subject: r.UserId, cents: r.AmountCents, currency: r.Currency, recurring: true}
		if in := purchase(p, r.TransactionId); in != nil {
			state(ctx, sweeps.lg, send, r.OrgName, in)
			payers = append(payers, p)
		}
	}
	customers(sweeps.lg, file, payers)
}

// purchase projects a settled payment onto the occurrence the plane stores. PURE —
// no clock, no I/O, no tenant lookup — so a test asserts exactly what leaves this
// process, which is the only way "the value is the money that moved" is a property
// rather than a promise.
//
// nil means there is nothing to state, on the two facts an occurrence cannot be
// filed without: the payer it concerns and the settlement that identifies it.
//
// # The dedup key IS the settlement's own reference
//
// `event_id` is what makes a browser pixel and this server-side conversion COUNT
// ONCE at the platform — apps/destinations reads it straight out of the properties
// and every adapter renders it (Meta's order_id, GA4's transaction_id, Pinterest's
// order_id). The value is [firstRef]'s answer, the processor's own reference for the
// charge, which is the identifier that is the same across every path that can credit
// one payment. So a settlement retried, replayed by a webhook or redelivered states
// the same id and converges on ONE conversion, exactly as it converges on one
// accrual and one deposit. A minted id would report the same sale as many.
//
// # The value is the money the customer actually paid
//
// Major units of the currency charged, from the minor-unit pair both mints declare
// ([payment.cents]) — never the scorer's nano-USD, which is converted and absent for
// any currency this process cannot state in USD. A conversion carrying a converted
// number reports the wrong revenue to the platform optimising on it, and one
// carrying nothing reports a sale worth nothing.
//
// The amount is the one the card was charged, and it is only ever stated once a
// charge really cleared — [screen.learn]'s rule, and it is the same rule here
// because a payer who inflates it inflates their own bill.
func purchase(p payment, ref string) *client.EventIn {
	if p.subject == "" || ref == "" {
		return nil
	}
	// WHAT THE ENDPOINT DID NOT OBSERVE IS NOT STATED, on either fact. A revenue of 0 is a
	// sale worth nothing, which a platform averages into its bidding, where an absent
	// one is a sale of unstated size; and an empty currency restated here would be a
	// second place deciding what empty means, when the translator already reads it as
	// USD (apps/destination/translate.go).
	attrs := []client.Signal{{Name: "event_id", Value: ref}}
	// The row names the BUYER when a person bought: the subject their browser is
	// identified by, so the sale joins their visits and the ad click they came
	// through. The wallet rides beside it as the payer, which is all a renewal
	// knows, and which the conversion plane uses to find the buyer again.
	subject := cmp.Or(p.person, p.subject)
	if p.subject != "" {
		attrs = append(attrs, client.Signal{Name: "payer", Value: p.subject})
	}
	if p.recurring {
		attrs = append(attrs, client.Signal{Name: "recurring", Value: "true"})
	}
	if p.test {
		// Test traffic states no amount and no unit price: nothing that reaches a
		// platform can be summed into revenue.
		attrs = append(attrs, client.Signal{Name: "test", Value: "true"})
	}
	if p.plan != "" {
		// What was bought, as the one line item GA4 and Meta read (item_id is the
		// plan id, item_category its ladder; the translator lifts these into
		// items[]). The line is the plan, so its quantity is one and its price the
		// revenue; the seats and a starting balance ride beside it.
		attrs = append(attrs,
			client.Signal{Name: "item_id", Value: p.plan},
			client.Signal{Name: "item_category", Value: cmp.Or(p.goods.tab, "personal")},
			client.Signal{Name: "item_variant", Value: cmp.Or(intervalName(p.interval), "monthly")},
			client.Signal{Name: "quantity", Value: "1"},
		)
		if p.coupon != "" {
			attrs = append(attrs, client.Signal{Name: "coupon", Value: p.coupon})
		}
		// The sale as the funnel reads it, server-side, so a sale counts whether or
		// not the buyer's page lived to say so: the org it is filed under, the
		// plan's ladder (tab), the seats, the order (the subscription it opened,
		// which is event_id too), how it was paid and whether it replaced a plan.
		attrs = append(attrs,
			client.Signal{Name: "org", Value: p.ledger},
			client.Signal{Name: "tab", Value: cmp.Or(p.goods.tab, "personal")},
			client.Signal{Name: "seats", Value: strconv.FormatInt(max(p.goods.seats, 1), 10)},
			client.Signal{Name: "order_id", Value: ref},
		)
		if p.goods.method != "" {
			attrs = append(attrs, client.Signal{Name: "payment_method", Value: p.goods.method})
		}
		if p.goods.setup != "" {
			attrs = append(attrs, client.Signal{Name: "setup", Value: p.goods.setup})
		}
		if p.cents > 0 && !p.test {
			attrs = append(attrs, client.Signal{Name: "amount_cents", Value: strconv.FormatInt(p.cents, 10)})
			if p.goods.balance > 0 {
				// The starting balance is prepaid usage, not the plan's revenue: it is
				// stated beside the revenue and never inside it.
				attrs = append(attrs, client.Signal{Name: "balance_cents", Value: strconv.FormatInt(p.goods.balance, 10)})
			}
		}
	}
	// The choice the buyer's checkout stated is the one the sale is sent under: a
	// sale that states none reaches no ad platform (apps/destination).
	if p.consent != nil {
		attrs = append(attrs, client.Signal{Name: "consent", Value: *p.consent})
	}
	if revenue := p.cents - p.goods.balance; revenue > 0 && !p.test {
		// Major units, in the currency's own scale: a yen has no minor unit.
		scale := math.Pow10(int(commercecurrency.Type(strings.ToLower(p.currency)).Decimals()))
		major := strconv.FormatFloat(float64(revenue)/scale, 'f', -1, 64)
		attrs = append(attrs, client.Signal{Name: "revenue", Value: major})
		if p.plan != "" {
			attrs = append(attrs, client.Signal{Name: "item_price", Value: major})
		}
	}
	if p.currency != "" {
		attrs = append(attrs, client.Signal{Name: "currency", Value: p.currency})
	}
	return &client.EventIn{
		Name:       orderCompleted,
		Product:    "commerce",
		Subject:    subject,
		Attributes: attrs,
	}
}

// intervalName is the billing interval the funnel spells it: month is monthly,
// year is annual.
func intervalName(interval string) string {
	switch strings.ToLower(interval) {
	case "year", "yearly", "annual":
		return "annual"
	case "month", "monthly", "":
		return "monthly"
	}
	return ""
}

// canceled states that a subscription was canceled, on the caller's own plane. It is
// a moment of the funnel like the sale is, and carries no money: the plan it ended,
// as the line item the platforms read, so a cancellation is countable by plan.
//
// It follows [screen.emit]'s posture: the customer has been answered, so it is
// detached, bounded by [emitBudget], dropped past [maxEmits] and never an error.
func canceled(org string, sub *commercebilling.Subscription) {
	if sub == nil || sub.UserID == "" || sub.PlanID == "" {
		return
	}
	select {
	case emitting <- struct{}{}:
	default:
		return
	}
	in := &client.EventIn{
		Name:    "subscription_canceled",
		Product: "commerce",
		Subject: sub.UserID,
		Attributes: []client.Signal{
			{Name: "plan", Value: sub.PlanID},
			{Name: "item_id", Value: sub.PlanID},
			{Name: "quantity", Value: strconv.Itoa(max(sub.Quantity, 1))},
		},
	}
	call, lg := send, sweeps.lg
	go func() {
		defer func() { <-emitting }()
		defer func() { _ = recover() }()
		state(context.Background(), lg, call, org, in)
	}()
}
