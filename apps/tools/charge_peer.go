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
// (marketplace.Mount), and the fleet runs ONE PROCESS PER APP — so in the tools
// binary it is nil, and a priced dispatch answered ErrChargerUnset: a permanent 402
// carrying no terms, which no client could ever satisfy. Prices were in the catalog
// and revenue was not.
//
// So the seam asks the process that owns the rail instead of assuming, exactly as
// resource_billing_peer.go asks commerce whether a priced create may run. The tool
// plane still knows nothing about money: it sends a resource id and the proof that
// arrived on the request, and reads back an outcome. What a call costs, who is paid
// and whether a signature verifies remain entirely on the other side.
//
// ONE POLICY, TWO TRANSPORTS. Dispatch offers EVERY call to the seam, free ones
// included, because "is this priced" is one lookup in the same table that settles
// and asking it twice is how a gate and a settlement come to disagree. That is
// unchanged here; only the wire is new.

const (
	peerX402 = "x402"

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
// ErrChargerUnset is the ONE fallback, and it is a fact rather than a guess:
// cloud.ErrNoPeer means the fleet does not run x402 at all, which is the same world
// the nil Charger describes — no payment rail here. Dispatch answers it the way it
// always has: a tool that DECLARES a price is refused, and a tool that declares none
// is free by its own statement, not by our silence. Every other failure — the rail
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
		in.Proof = strings.TrimSpace(c.Header(plane.HeaderProof))
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
		if payer := principal.Ledger(c); payer != "" {
			call = cloud.As(c, payer)
		} else {
			call = cloud.For(context.Background(), "")
		}
	}

	call, cancel := context.WithTimeout(call, settleTimeout)
	defer cancel()

	out, err := cloud.Ask[plane.SettleIn, plane.Settled](call, peerX402, plane.X402Settle, &in)
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return ErrChargerUnset
	case err != nil:
		return fmt.Errorf("tools: payment rail: %w", err)
	case out == nil:
		// A void reply from a payment rail is not permission. Nothing said paid.
		return errors.New("tools: payment rail answered nothing")
	}

	// The challenge and the receipt are HEADERS on the response this process is
	// writing, so they are put back on it here — the rail had no response to write
	// them to. A 402 whose terms never reach the client is a refusal it cannot act
	// on, which is the exact defect this whole change exists to close.
	if c != nil {
		if out.Challenge != "" {
			c.SetHeader(plane.HeaderRequirements, out.Challenge)
		}
		if out.Receipt != "" {
			c.SetHeader(plane.HeaderReceipt, out.Receipt)
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
