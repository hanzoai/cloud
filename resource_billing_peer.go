// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/money"
)

// The money plane reached from a process that does not own it.
//
// The prepaid ledger is per-org SQLite with a single writer, so exactly one
// process opens it — the one that mounts commerce. Every other app therefore has
// no metering client, and ResourceMeter's documented behaviour for that is to
// ALLOW: correct for a deployment that does not bill, and a free-work hole for one
// that does. Splitting apps into their own binaries turned every priced create
// free without changing a line of billing code.
//
// So the gate asks commerce over the internal plane instead of assuming. The
// input deliberately mirrors metering.AuthInput minus the parts a caller must
// not choose: the billed ORG rides the caller, never the argument.
const (
	peerCommerce    = "commerce"
	peerCallTimeout = 10 * time.Second
)

// gatePeer asks the process that owns the ledger whether this act may run.
//
// It returns the SAME errors the local gate does, so DenyResource renders one
// contract whichever side answered: out-of-funds is 402 insufficient_balance, a
// cap is 402 spend_cap_exceeded, and anything else is unknown — which the
// fail-closed caller turns into 503 rather than free work.
func (rm *ResourceMeter) gatePeer(ctx context.Context, org, project string, projectValidated bool, costCents int64) error {
	if !PeerPresent(peerCommerce) {
		// No local ledger AND no commerce serving one: this deployment has no money
		// plane at all, which is a legitimate shape and the behaviour "billing not
		// configured" always had. Refusing here would 503 every priced act in a
		// deployment that never intended to bill.
		//
		// It is NOT the free-work hole: where commerce DOES run, its socket exists,
		// so this branch is not reached and an unreachable biller still fails closed
		// below. The distinction is "nobody bills here" versus "the biller is one
		// socket away", and the socket is what answers it.
		return nil
	}
	in := plane.AuthorizeIn{
		Subject:          org,
		Amount:           plane.Amount(money.FromUSD(costCents)),
		Project:          project,
		Service:          rm.provider,
		ProjectValidated: projectValidated,
	}
	ctx, cancel := context.WithTimeout(For(ctx, org), peerCallTimeout)
	defer cancel()

	v, err := Ask[plane.AuthorizeIn, plane.Verdict](ctx, peerCommerce, plane.FinanceAuthorize, &in)
	if err != nil {
		// The biller is unreachable. Unknown, never allowed.
		return fmt.Errorf("gate: commerce unreachable: %w", err)
	}
	switch {
	case v == nil:
		// A void reply from a gate is not permission. Nothing said yes.
		return fmt.Errorf("gate: commerce answered nothing")
	case v.OK:
		return nil
	case v.NoFunds:
		return metering.ErrInsufficientBalance
	case v.CapSpent:
		return metering.ErrSpendCapExceeded
	default:
		return fmt.Errorf("gate: %s", v.Reason)
	}
}

// meterPeer debits through the process that owns the ledger.
//
// Fire-and-forget on a background context, exactly like the local debit: the
// resource already exists, so the charge must never block the response the caller
// received, and a request cancellation must not cancel the money. A failure is
// logged for reconciliation rather than swallowed — an unbilled create is a number
// somebody has to find later, so it says so now.
func (rm *ResourceMeter) meterPeer(org, kind string, u metering.Usage) {
	if !PeerPresent(peerCommerce) {
		return // nothing bills in this deployment; there is no debit to lose
	}
	in := plane.RecordIn{
		Subject: org,
		Amount:  plane.Amount(money.FromUSD(u.AmountCents)),
		Usage: plane.Usage{
			Project: u.Project,
			Service: firstNonEmpty(u.Service, rm.provider),
		},
	}
	log := rm.log
	go func() {
		// The debit acts FOR the org with no request behind it, so it states the
		// tenant explicitly — the books it writes to are chosen here, not by
		// whatever ran last.
		ctx, cancel := context.WithTimeout(For(context.Background(), org), peerCallTimeout)
		defer cancel()
		if _, err := Ask[plane.RecordIn, plane.Recorded](ctx, peerCommerce, plane.FinanceRecord, &in); err != nil && log != nil {
			log.Error("resource debit failed over the internal plane (resource created, not billed)",
				"org", org, "kind", kind, "cents", u.AmountCents, "err", err)
		}
	}()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
