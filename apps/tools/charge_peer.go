package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
)

// The payment rail reached from a process that does not contain it.
//
// The Charger is installed by the subsystem that owns the price table
// (marketplace.Use), and the fleet runs ONE PROCESS PER APP — so in the tools
// binary it is nil, and a priced dispatch answered ErrChargerUnset: a permanent 402
// carrying no terms, which no client could ever satisfy. Prices were in the catalog
// and revenue was not.
//
// So the client asks the process that owns the rail instead of assuming, exactly as
// resource_billing_peer.go asks commerce whether a priced create may run. The tool
// plane still knows nothing about money: it sends a resource id and the proof that
// arrived on the request, and reads back an outcome. What a call costs, who is paid
// and whether a signature verifies remain entirely on the other side.
//
// ONE POLICY, TWO TRANSPORTS. Dispatch offers EVERY call to the client, free ones
// included, because "is this priced" is one lookup in the same table that settles
// and asking it twice is how a gate and a settlement come to disagree. That is
// unchanged here; only the wire is new.

const (
	peerX402 = "x402"

	// peerMarketplace owns the price table, which is a DIFFERENT fact from the rail
	// and is asked for separately — see railless, the one place it is reached from
	// here, and the only thing this process ever learns about money.
	peerMarketplace = "marketplace"

	// settleTimeout bounds the hop a CLIENT is holding open, and it is derived
	// rather than picked: the rail's own hops — price, payee, debit, credit — are
	// 10s each (apps/x402/peer.go), so 60s leaves every one of them room to answer
	// and still refuse first, which is what keeps a diagnosable "payee_unavailable"
	// from arriving as an opaque timeout on the hop that was only waiting.
	//
	// It has to be explicit because nothing else bounds it. A fiber Ctx.Context()
	// carries no deadline, so both contexts this builds are deadline-free, and the
	// only remaining limits were a transport read timeout stacked on the wake
	// ceiling — two minutes of a tool request and its goroutine held open.
	settleTimeout = 60 * time.Second
)

// chargePeer settles one tool call through the x402 process.
//
// It returns the same three outcomes the local Charger does, and they mean the same
// things: nil is "serve it" (paid, or free), ErrPaymentRequired is "the client has
// not paid and the challenge is on the response", and anything else fails the call
// CLOSED.
//
// A rail that cannot be reached is NOT a tool that costs nothing, and that is the
// whole of railless below: cloud.ErrNoPeer says only that no x402 answers here, and
// what a tool costs was never x402's to know anyway. Every other failure — the rail
// down, a peer that answered badly — is an outage and is returned as itself.
func chargePeer(ctx context.Context, tool string) error {
	// The request carries the two things the rail cannot derive: WHO pays, and the
	// proof they signed. Off the HTTP path there is neither, and the rail refuses a
	// priced resource on those terms — which is the same answer as before, reached
	// the same way.
	c, _ := cloud.Request(ctx)

	in := plane.SettleIn{Resource: plane.ToolResource(tool)}
	call := ctx
	if c != nil {
		in.Payment = strings.TrimSpace(c.Header(plane.HeaderPaymentSignature))
		// The payer is resolved HERE, by the one resolver that knows the rule —
		// principal.Ledger, which folds in the SuperAdmin masquerade — and delegated
		// as the tenant the settlement acts for. cloud.As, never cloud.For: inside a
		// typed handler zip prefers the inbound assertion and drops a stated caller,
		// so For would silently ship the SELECTED org and a masquerading admin would
		// spend the inspected tenant's ledger.
		//
		// An UNBILLABLE request delegates NOTHING. cloud.As with an empty org keeps
		// the caller's own tenant — the raw X-Org-Id — so forwarding it here would
		// hand the rail a payer that principal.Ledger had just refused to name, and
		// an unvalidated header would become a ledger. A context with no request and
		// no stated tenant is the honest form: a free tool still settles for nothing,
		// and a priced one is refused for want of a payer.
		// The TENANT, not the payer: cloud.As names whose books the settlement acts
		// for, which is the org. principal.Payer (the wallet within it) is the gate's
		// and the meter's question, asked in typed.go and http.go — a wallet key here
		// would name an org that does not exist.
		if tenant := principal.Ledger(c); tenant != "" {
			call = cloud.As(c, tenant)
		} else {
			call = cloud.For(context.Background(), "")
		}
	}

	call, cancel := context.WithTimeout(call, settleTimeout)
	defer cancel()

	out, err := cloud.Ask[plane.SettleIn, plane.Settled](call, peerX402, plane.X402Settle, &in)
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return railless(call, in.Resource)
	case err != nil:
		return fmt.Errorf("tools: payment rail: %w", err)
	case out == nil:
		// A void reply from a payment rail is not permission. Nothing said paid.
		return errors.New("tools: payment rail answered nothing")
	}

	// The challenge and the settlement are HEADERS on the response this process is
	// writing, so they are put back on it here — the rail had no response to write
	// them to. A 402 whose terms never reach the client is a refusal it cannot act
	// on, which is the exact defect this whole change exists to close.
	//
	// They cross the plane ALREADY ENCODED and are set verbatim: re-rendering them
	// here would be a second encoder of the x402 wire in a package that deliberately
	// knows nothing about it.
	if c != nil {
		if out.Challenge != "" {
			c.SetHeader(plane.HeaderPaymentRequired, out.Challenge)
		}
		if out.Response != "" {
			c.SetHeader(plane.HeaderPaymentResponse, out.Response)
		}
	}

	switch {
	case out.OK:
		return nil
	case out.Status == http.StatusPaymentRequired:
		return fmt.Errorf("%w: %s (%s)", ErrPaymentRequired, out.Reason, out.Code)
	default:
		return fmt.Errorf("tools: payment refused: %s (%s)", out.Reason, out.Code)
	}
}

// railless decides what "no x402 answers here" MEANS for ONE tool, because on its
// own it means nothing about money.
//
// THE RAIL IS NOT THE PRICE TABLE. cloud.ErrNoPeer reports a deployment with no
// x402 in it; what a tool COSTS is the marketplace's listing, and this process does
// not hold one — in the split fleet the registry row carries no Price at all, so
// "the row declares none" is silence rather than an answer. Reading the rail's
// absence as "nothing is priced" therefore gave away precisely the tools that earn:
// x402 stops answering, every listed tool dispatches for nothing, and the only trace
// is revenue that is not there.
//
// So the OWNER is asked, and its answer decides:
//
//   - FOR SALE ⇒ refused as the outage it is. A price nobody can collect is not a
//     discount, and no challenge can be issued for a rail that is gone — so this is
//     returned as itself and never as a 402 the client could act on.
//   - not priced ⇒ ErrChargerUnset, which is also the answer when the table is not
//     in this deployment either: neither a rail nor a table is a deployment that
//     sells nothing, and Dispatch then weighs the one price evidence left, the tool
//     row's own declaration.
//
// It asks a SECOND question rather than repeating the first: the settlement was not
// made, so there is no settlement for a gate to disagree with — only "is there money
// here I cannot collect", which nothing else in this process can answer.
func railless(ctx context.Context, resource string) error {
	out, err := cloud.Ask[plane.PriceIn, plane.Priced](ctx, peerMarketplace, plane.MarketPrice,
		&plane.PriceIn{Resource: resource})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return ErrChargerUnset
	case err != nil:
		return fmt.Errorf("tools: no payment rail, and the price table: %w", err)
	case out == nil:
		// A void reply from a price table is not "free". Nothing answered.
		return errors.New("tools: no payment rail, and the price table answered nothing")
	case out.Priced:
		return fmt.Errorf("tools: %s is for sale and no payment rail is reachable", resource)
	}
	return ErrChargerUnset
}
