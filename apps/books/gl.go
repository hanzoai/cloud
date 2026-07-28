package books

// gl.go — the double-entry MODEL and the pure posting pipeline ported from ERPNext's
// erpnext/accounts/general_ledger.py::process_gl_map. It is deliberately DB-free: given
// a raw list of legs it returns the balanced, normalized legs a store will persist, or
// an error if they cannot balance. Keeping the accounting SEMANTICS in a pure function
// means the choke point (store.post) is the ONLY writer and this is the ONLY arithmetic
// — one place to reason about "do the books balance".
//
// MONEY MATH. Amounts are int64 MINOR UNITS (USD cents). Never float64: cents are exact
// under add/subtract/negate, the only operations double-entry needs, so there is no
// rounding error to accumulate and no precision table to carry. A fractional-cent input
// cannot occur — commerce transacts whole cents — so integer cents is the exact model.

import "fmt"

// RoundOffAllowance bounds the debit−credit difference process gl will absorb into the
// round-off account instead of rejecting. With exact integer cents a well-formed voucher
// nets to zero, so this only ever soaks up a 1–2¢ artifact of an upstream split; a larger
// gap is a real imbalance and MUST fail closed rather than silently plug equity.
const RoundOffAllowance int64 = 2

// Leg is one side of a posting: an account and its debit AND credit in cents. A caller
// sets exactly one of the two; the pipeline normalizes anything else (a merge can leave
// both set, which toggle collapses to a single side).
type Leg struct {
	Account string `json:"account"`
	Debit   int64  `json:"debit"`
	Credit  int64  `json:"credit"`
}

// Voucher is one accounting EVENT: a set of legs that must balance, tagged with its
// idempotency key (SourceKind, SourceID) so the same source event posts exactly once.
type Voucher struct {
	SourceKind  string `json:"sourceKind"`
	SourceID    string `json:"sourceId"`
	PostingAt   string `json:"postingAt"`
	Description string `json:"description"`
	Legs        []Leg  `json:"legs"`
}

// processGLMap replicates general_ledger.py's process_gl_map in order:
//
//  1. merge_similar_entries — collapse legs on the SAME account, summing debit + credit.
//  2. toggle_debit_credit_if_negative — a leg whose net (debit−credit) is negative is
//     flipped to the opposite side so no leg carries a negative amount, and net-zero legs
//     are dropped (they carry no accounting weight).
//  3. process_debit_credit_difference — compute Σdebit − Σcredit; absorb a |diff| ≤
//     allowance into a round-off leg, else reject as unbalanced.
//  4. the invariant: Σdebit == Σcredit, or it is a programming error and we refuse.
//
// It returns the normalized, balanced legs to persist. roundOff is the account the
// difference is booked to (books.RoundOff).
func processGLMap(raw []Leg, allowance int64, roundOff string) ([]Leg, error) {
	merged := mergeSimilar(raw)
	legs := toggleNegative(merged)

	diff := debitCreditDiff(legs)
	if diff != 0 {
		if abs64(diff) > allowance {
			return nil, fmt.Errorf("books: voucher does not balance: debit−credit=%d cents exceeds round-off allowance %d", diff, allowance)
		}
		// diff>0 ⇒ debits exceed ⇒ book the excess as a CREDIT to round-off (and vice
		// versa), which is exactly the leg that drives the difference to zero.
		if diff > 0 {
			legs = append(legs, Leg{Account: roundOff, Credit: diff})
		} else {
			legs = append(legs, Leg{Account: roundOff, Debit: -diff})
		}
	}

	if d := debitCreditDiff(legs); d != 0 {
		return nil, fmt.Errorf("books: internal imbalance after round-off: %d cents", d)
	}
	if len(legs) == 0 {
		return nil, fmt.Errorf("books: empty voucher (no non-zero legs)")
	}
	return legs, nil
}

// mergeSimilar sums debit and credit per account, preserving first-seen order so the
// persisted legs read in the order the caller supplied them.
func mergeSimilar(raw []Leg) []Leg {
	idx := map[string]int{}
	out := make([]Leg, 0, len(raw))
	for _, l := range raw {
		if i, ok := idx[l.Account]; ok {
			out[i].Debit += l.Debit
			out[i].Credit += l.Credit
			continue
		}
		idx[l.Account] = len(out)
		out = append(out, l)
	}
	return out
}

// toggleNegative reduces each leg to a single non-negative side by its net (debit −
// credit): net>0 ⇒ pure debit, net<0 ⇒ pure credit, net==0 ⇒ dropped. This is
// process_gl_map's toggle_debit_credit_if_negative: it guarantees no persisted leg
// carries a negative amount, so debit/credit columns are always ≥ 0.
func toggleNegative(in []Leg) []Leg {
	out := make([]Leg, 0, len(in))
	for _, l := range in {
		net := l.Debit - l.Credit
		switch {
		case net > 0:
			out = append(out, Leg{Account: l.Account, Debit: net})
		case net < 0:
			out = append(out, Leg{Account: l.Account, Credit: -net})
		}
	}
	return out
}

// debitCreditDiff is Σdebit − Σcredit over the legs (zero iff the voucher balances).
func debitCreditDiff(legs []Leg) int64 {
	var d int64
	for _, l := range legs {
		d += l.Debit - l.Credit
	}
	return d
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
