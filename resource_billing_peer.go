// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/plane/commerce"
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
const peerCommerce = "commerce"

// ledgerCallTimeout bounds EVERY call this file and its sibling make to the money
// plane — the gate, and both legs of the debit.
//
// The debit's bound is the load-bearing one. A debit now owns the commitment it
// settles (see Charge.Debit), so an unbounded call would hold that commitment for
// as long as commerce is unwell — and a commitment outstanding is money the wallet
// cannot spend. An unreachable ledger would stop billing a customer AND lock them
// out of their own balance, which is the worse of the two failures by far. Bounded,
// the worst case is a debit that is logged and not written: we lose the charge, the
// customer keeps their money, and the gate goes on working.
//
// A var, not a const, so a test can drive the timeout path in milliseconds rather
// than making the suite wait out a real one.
var ledgerCallTimeout = 10 * time.Second

// gatePeer asks the process that owns the ledger whether this act may run.
//
// It returns the SAME errors the local gate does, so DenyResource renders one
// contract whichever side answered: out-of-funds is 402 insufficient_balance, a
// cap is 402 spend_cap_exceeded, and anything else is unknown — which the
// fail-closed caller turns into 503 rather than free work.
func (rm *ResourceMeter) gatePeer(ctx context.Context, payer account.Account, project string, projectValidated bool, costCents int64) error {
	in := plane.AuthorizeIn{
		Subject:          payer.Subject(),
		Amount:           plane.Amount(money.FromUSD(costCents)),
		Project:          project,
		Service:          rm.provider,
		ProjectValidated: projectValidated,
	}
	// Subject is the WALLET, For() names the BOOKS that hold it — the two halves the
	// address carries. Passing the wallet key as the tenant would ask commerce for an
	// org named "hanzo/stranger", which is no org.
	ctx, cancel := context.WithTimeout(For(ctx, payer.Org()), ledgerCallTimeout)
	defer cancel()

	v, err := commerce.FinanceAuthorize(ctx, &in)
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
func (rm *ResourceMeter) meterPeer(payer account.Account, kind string, u metering.Usage, posted func()) {
	// The EXACT debit, never the cents field. plane.Money is a decimal string
	// precisely so a debit crosses the process boundary unrounded, and the
	// receiver honors that (meter_rpc.go parses the decimal and debits it
	// verbatim) — but this sender flattened first, so a usage priced only as a
	// typed Amount arrived as $0.00. Usage.Money resolves the three amount
	// sources with their documented precedence; it is the same question
	// MeterUsage already asks to decide there is anything to bill at all.
	in := plane.RecordIn{
		Subject: payer.Subject(),
		Amount:  plane.Amount(u.Money().Unwrap()),
		Usage: plane.Usage{
			// THE WHOLE ROW CROSSES, not the three fields it takes to move money.
			// [metering.Client.Record] carries twelve over this identical crossing;
			// this leg carried three, so every debit taken in a split deploy landed as
			// an undifferentiated line item — no model, so a bill could not say WHICH
			// unit was bought; no actor, so an audit could not say whose hand moved it;
			// no request id or address, so a disputed charge could not be traced back
			// to the call. Same silent field loss the Ref note below describes, and
			// invisible the same way: a dropped field is not an error, it is a blank.
			Model:    u.Model,
			Project:  u.Project,
			Provider: cmp.Or(u.Provider, rm.provider),
			Service:  cmp.Or(u.Service, rm.provider),
			// THE ACT'S NAME CROSSES WITH IT. The receiver keys the debit on this ref
			// and mints a fresh one when it is absent (apps/finance RecordUsage), so a
			// crossing that drops it turns every re-drive of ONE act into a SECOND
			// debit. A surface that already holds the act's server-assigned name sets
			// it precisely so that cannot happen — apps/company mints the formation ref
			// and hands the same value to the debit — and that promise held only while
			// the ledger was in this process. It is the same field the metering client
			// carries over the same crossing; the two paths now name an act alike.
			Ref: u.Ref,
			// Correlation and attribution: WHO acted, from where, on which call, and
			// the counts the amount was computed from — so a debit can be re-derived
			// and not merely re-read.
			RequestID:    u.RequestID,
			ClientIP:     u.ClientIP,
			Actor:        u.Actor,
			PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			TotalTokens: u.TotalTokens,
		},
	}
	log := rm.log
	settle(posted, log, payer.Subject(), kind, func() {
		// The debit acts FOR the org with no request behind it, so it states the
		// tenant explicitly — the books it writes to are chosen here, not by
		// whatever ran last.
		ctx, cancel := context.WithTimeout(For(context.Background(), payer.Org()), ledgerCallTimeout)
		defer cancel()
		if _, err := commerce.FinanceRecord(ctx, &in); err != nil && log != nil {
			log.Error("resource debit failed over the internal plane (resource created, not billed)",
				"payer", payer.Subject(), "books", payer.Org(), "kind", kind, "amount", u.Money().String(), "err", err)
		}
	})
}
