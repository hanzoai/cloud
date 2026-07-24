package books

// bank_reconcile.go — matching a bank INFLOW against the P0 ledger's Square-clearing
// settlements. This is the guard against DOUBLE-COUNTING revenue: when Square captures a
// charge, the spine books Dr 1010 Square-clearing / Cr 2000 (a settlement asset). Days
// later the cash lands in the real bank as an inflow. That inflow is NOT new revenue — the
// revenue was already recognized at usage in the spine — it is the SAME money arriving, so
// it clears 1010 → 1000 and touches no income account. Reconciliation is what tells a
// genuine new deposit (book it) apart from a settlement arriving (clear it).
//
// THE MATCH KEY is two gates, both required — amount coincidence alone is NOT a match:
//
//  1. PROCESSOR IDENTITY. The inflow's descriptor must name the payment processor whose
//     captures clear through 1010 (Square/Stripe). An owner contribution, a loan draw, a
//     refund received, or genuinely new same-amount revenue carries no processor descriptor,
//     so it is never mistaken for a settlement — it stays UNMATCHED and raises a question.
//  2. A SPECIFIC UNCONSUMED CAPTURE. The inflow must claim one exact commerce capture (a
//     DEBIT on 1010 booked when the processor captured the charge) of the same amount, posted
//     within reconcileWindow before it, that NO prior settlement has consumed. Claiming a
//     specific capture — not an aggregate 1010 balance — means a coincidental same-amount
//     inflow can never pre-consume the capture the real settlement still needs, and a second
//     identical settlement finds no open capture and raises a question instead of over-clearing.
//
// It is intentionally conservative: any inflow that fails EITHER gate stays UNMATCHED and
// becomes a clarifying question rather than a guessed clearing (fail secure — never invent
// revenue, never misbook equity/loan cash as a settlement).

import (
	"context"
	"strings"
	"time"
)

// reconcileWindow is how far BEFORE a bank inflow a Square capture may sit and still be its
// settlement. ACH/card settlement to a bank typically lands within a few business days; a
// week bounds that generously while keeping an unrelated same-amount capture from a month
// earlier from being mistaken for the settlement.
const reconcileWindow = 7 * 24 * time.Hour

// reconStatus is the outcome of trying to reconcile one inflow.
type reconStatus struct {
	Matched   bool   // both gates passed: a specific unconsumed capture was claimed
	CaptureID string // the commerce capture (voucher source_id) this inflow cleared, when Matched
	From      string // window start (RFC3339), for surfacing why a match did or did not happen
	To        string // window end == the inflow's posting time
}

// matchInflow decides whether a bank inflow reconciles a prior processor capture. It returns
// Matched=true only when the inflow passes BOTH gates: (1) its descriptor names the payment
// processor, and (2) it can claim a SPECIFIC unconsumed capture of the exact amount in the
// [posted−window, posted] window. Failing either gate yields Matched=false — the caller then
// raises a clarifying question rather than clearing. A malformed PostedAt yields no match
// (fail closed: an inflow we cannot place in time is not confidently a settlement).
func matchInflow(ctx context.Context, st *store, bt BankTxn) (reconStatus, error) {
	posted, err := time.Parse(time.RFC3339, bt.PostedAt)
	if err != nil {
		return reconStatus{}, nil
	}
	from := posted.Add(-reconcileWindow).UTC().Format(time.RFC3339)
	to := posted.UTC().Format(time.RFC3339)
	rs := reconStatus{From: from, To: to}

	// Gate 1: processor identity. Only an inflow that names the processor can be a
	// settlement; anything else (owner wire, loan, refund received, new revenue) of a
	// coincidentally-equal amount is NOT guessed into a clearing — it stays unmatched.
	if !isSettlement(bt) {
		return rs, nil
	}
	// Gate 2: claim a specific unconsumed capture. This is atomic and idempotent per inflow,
	// so a coincidental same-amount inflow can never pre-consume the real settlement's capture
	// and a re-sync never double-consumes.
	captureID, ok, err := st.claimCapture(ctx, bt.AmountCents, from, to, bt.Connector, bt.ExternalID, bt.PostedAt)
	if err != nil {
		return rs, err
	}
	rs.Matched = ok
	rs.CaptureID = captureID
	return rs, nil
}

// isSettlement reports whether a bank inflow's descriptor names the payment processor whose
// captures clear through 1010 Square-clearing. This is the stronger-than-amount key that
// separates a real settlement from a coincidental same-amount deposit: a processor payout
// carries the processor's name in its merchant/description; an owner contribution, loan
// draw, or refund received does not. When it does not name the processor, the inflow is not
// a settlement candidate and must raise a question rather than clear.
func isSettlement(bt BankTxn) bool {
	hay := strings.ToLower(bt.Merchant + " " + bt.Description)
	return containsAny(hay, "square", "stripe")
}
