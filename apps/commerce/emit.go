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
// # Detached, bounded, dropped under pressure
//
// The money has moved and the customer has been answered by the time this runs, so
// it is [screen.learn]'s posture exactly: its own budget, a ceiling on how many may
// be in flight, a drop rather than a queue past it, panic-guarded, and every error
// logged and discarded. A conversion row is expendable; a settled payment is not.

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
)

// orderCompleted is the name the sale is filed under, and it is a CONSTANT because
// `name` is the column every lens groups by. It is the canonical events vocabulary's
// own word for a completed sale — what /v1/event/insights counts as an order and what
// apps/destinations translates into each platform's Purchase.
const orderCompleted = "order_completed"

// emitBudget bounds ONE statement end to end, including waking a cold analytics
// child (client.Call single-flights the start). It is generous for [teachBudget]'s
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

// emit states a settled payment as a completed order on the calling organisation's
// own event plane.
func (s screen) emit(p payment, ref string) {
	in := purchase(p, ref)
	if in == nil {
		return
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
	call := send
	go func() {
		defer func() { <-emitting }()
		defer func() {
			if r := recover(); r != nil {
				lg.Error("stating a settled payment on the event plane panicked", "err", r)
			}
		}()
		// THE TENANT IS STATED, for [screen.learn]'s reason: cloud.For on a
		// request-derived context prefers the gateway's assertion and returns before it
		// reads a stated one, and the org that PAYS is not always the org that asked.
		ask, cancel := context.WithTimeout(cloud.For(context.Background(), org), emitBudget)
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
	}()
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
	if p.cents > 0 {
		attrs = append(attrs, client.Signal{
			Name:  "revenue",
			Value: strconv.FormatFloat(float64(p.cents)/100, 'f', -1, 64),
		})
	}
	if p.currency != "" {
		attrs = append(attrs, client.Signal{Name: "currency", Value: p.currency})
	}
	return &client.EventIn{
		Name:       orderCompleted,
		Product:    "commerce",
		Subject:    p.subject,
		Attributes: attrs,
	}
}
