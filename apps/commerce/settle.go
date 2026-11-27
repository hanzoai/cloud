// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// settle.go — WHAT A SETTLED TOP-UP LEFT, READ BACK FROM THE LEDGER.
//
// This file used to deposit a settled card payment into the finance ledger. It no
// longer writes anything: commerce is the only writer of a payment's credit. It posts
// the spendable entry itself, to the payer's ledger this host names (payerLedger), once,
// keyed on the processor's reference, before it answers. What remains here is reading:
//
//   - [receiptOf] reads the receipt commerce wrote, for the books the charge was in;
//   - [screen.balanceAfter] reads the balance the credit left, from the finance ledger,
//     which is the one balance source GET /v1/billing/balance reports.

import (
	"context"
	"fmt"

	financeclient "github.com/hanzoai/cloud/finance"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// settlement is what a payment that CLEARED actually was, as its own receipt
// records it: the amount that moved, the currency it moved in, and which books it
// credited. Every field is a fact about the PAST — none of them is read off the
// request that asked for the charge, which is the whole reason this type exists
// rather than the handler reusing the values it was sent.
type settlement struct {
	// cents is the amount that settled, in the currency's minor unit.
	cents int64
	// currency is the ISO code the charge settled in.
	currency string
	// test reports which books were credited: the sandbox ones (a Square sandbox
	// charge) or live money.
	test bool
	// memo is the receipt's own note, carrying the processor and its reference. It
	// is carried over so the customer's finance page names the same payment the
	// commerce receipt does.
	memo string
}

// receiptOf reads that record out of the books the CHARGE WAS WRITTEN IN.
//
// It is [commercebilling.ReadPayment] — the module's own published read of the row
// its money core wrote — resolved through commerce's own org resolver, which is the
// same binding the charge itself used. The org is [payment.org], the effective org the
// handler charged through, so the read doubles as a check that the money core really
// wrote this receipt in the namespace the endpoint was acting in: a receipt that is
// not there is not found, and a not-found receipt refuses rather than funding a
// wallet on an unverified amount. It is NOT the payer's org — see the package note;
// those are the same string for every caller but a masquerading SuperAdmin, and
// naming the payer here is what made that caller's top-up permanently uncreditable.
//
// It is held on the screen rather than called directly for the one thing that
// buys: an endpoint test can state what settled without standing up a commerce
// datastore, exactly as the risk plane's [teach] client lets one assert what leaves
// this process without a risk child to receive it. The production value is set
// once, by [riskGate], and never varies.
func receiptOf(ctx context.Context, org, id string) (settlement, error) {
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		return settlement{}, fmt.Errorf("resolve org %q: %w", org, err)
	}
	rec, fault := commercebilling.ReadPayment(ctx, o, id)
	if fault != nil {
		return settlement{}, fault
	}
	if rec == nil {
		return settlement{}, fmt.Errorf("the receipt read answered nothing")
	}
	return settlement{
		cents:    rec.AmountCents,
		currency: rec.Currency,
		test:     rec.Test,
		memo:     rec.Notes,
	}, nil
}

// balanceAfter is the balance a settled top-up left at the address it was credited,
// read the way GET /v1/billing/balance reads it: the finance ledger's total for
// (payer ledger, subject), floored to whole cents, in the books that route reads.
//
// It is the ONE number a top-up answers. Commerce's money core has none to give: its
// own per-subject table is a different book, and an answer read from it said 4000
// where this ledger said 3500. The credit happens here ([screen.settle]), so only here
// is the balance after it known. ok is false when the ledger cannot say, and the
// answer then carries no balance — unknown is not zero.
func (s screen) balanceAfter(ctx context.Context, p payment) (int64, bool) {
	fin, err := books("read the balance a top-up left")
	if err != nil {
		return 0, false
	}
	funder, is := fin.(financeclient.Funder)
	if !is {
		return 0, false
	}
	f, err := funder.Funds(ctx, p.ledger, p.subject, sandboxed(false))
	if err != nil {
		s.lg.Warn("a settled top-up credited the ledger and its balance could not be read",
			"ledger", p.ledger, "subject", p.subject, "err", err)
		return 0, false
	}
	return f.Total().CentsDown(), true
}
