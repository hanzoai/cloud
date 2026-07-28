// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"fmt"
	"github.com/hanzoai/money"
	"time"

	"github.com/hanzoai/cloud/apps/metering"
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
// payload deliberately mirrors metering.AuthInput minus the parts a caller must
// not choose: the billed ORG rides the capability, never the body.
//
// These names are the commerce side's; they are duplicated here rather than
// imported because package cloud is what apps/commerce imports, and taking the
// dependency back would be a cycle. The shapes are one JSON contract, pinned by a
// test on each side.
const (
	peerCommerce    = "commerce"
	peerAuthorize   = "finance.authorize"
	peerRecord      = "finance.record"
	peerCallTimeout = 10 * time.Second
)

// gatePeer asks the process that owns the ledger whether this act may run.
//
// It returns the SAME errors the local gate does, so DenyResource renders one
// contract whichever side answered: out-of-funds is 402 insufficient_balance, a
// cap is 402 spend_cap_exceeded, and anything else is unknown — which the
// fail-closed caller turns into 503 rather than free work.
func (rm *ResourceMeter) gatePeer(ctx context.Context, org, project string, projectValidated bool, costCents int64) error {
	body := PutAuthorizeReq(org, money.FromUSD(costCents), project, rm.provider, projectValidated)
	ctx, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()

	out, err := Dial(peerCommerce).For(org).Call(ctx, peerAuthorize, body)
	if err != nil {
		// The biller is unreachable. Unknown, never allowed.
		return fmt.Errorf("gate: commerce unreachable: %w", err)
	}
	v, err := GateVerdict(out)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	switch {
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
	body := PutAuthorizeReq(org, money.FromUSD(u.AmountCents), u.Project,
		firstNonEmpty(u.Service, rm.provider), false)
	log := rm.log
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), peerCallTimeout)
		defer cancel()
		if _, err := Dial(peerCommerce).For(org).Call(ctx, peerRecord, body); err != nil && log != nil {
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
