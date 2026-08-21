package books

// report.go — the Trial Balance: the classic proof that the books balance. For every
// account it presents opening, period movement, and closing as debit/credit columns on
// the account's normal side, then the whole-ledger totals. The invariant the report
// EXISTS to prove is TotalDebit == TotalCredit — if that ever fails the ledger is broken,
// so Balanced is computed, never assumed.

import "context"

// TrialBalanceRow is one account's line: opening + period movement → closing, each split
// onto its debit/credit column by the SIGN of its net (debit − credit): a positive net is
// a debit balance, a negative net a credit balance. Type is carried for presentation (the
// account's normal side) but does not affect placement — a faithfully-signed net is shown
// truthfully, so a contra-balance (e.g. an overdrawn wallet) reads as it really is.
type TrialBalanceRow struct {
	// Account is the chart-of-accounts NUMBER this line reports on ("1000", "4000")
	// — the stable posting key, not a display label.
	Account string `json:"account"`
	// Name is that account's human name from the fixed chart.
	Name string `json:"name"`
	// Type is the account's fundamental class — asset, liability, income, expense or
	// equity — which is also its normal balance side. It is carried for presentation
	// and does NOT decide which column an amount lands in: placement follows the
	// sign of the real net, so a contra balance shows up as one.
	Type AccountType `json:"type"`
	// OpeningDebit is the account's balance before the window began, in whole cents,
	// when that balance was on the debit side. Zero when the balance was a credit
	// one — the pair is exclusive, never two halves of one number.
	OpeningDebit int64 `json:"openingDebit"`
	// OpeningCredit is the same opening balance in cents when it fell on the credit
	// side. Zero when the balance was a debit one.
	OpeningCredit int64 `json:"openingCredit"`
	// Debit is the account's MOVEMENT within the window — closing minus opening, not
	// the closing balance — in cents, when that movement was net debit. Zero when the
	// account moved net credit.
	Debit int64 `json:"debit"`
	// Credit is the same window movement in cents when it was net credit.
	Credit int64 `json:"credit"`
	// ClosingDebit is the balance at the end of the window, in cents, when it is a
	// debit balance. This is the column the report's totals are summed from.
	ClosingDebit int64 `json:"closingDebit"`
	// ClosingCredit is that closing balance in cents when it is a credit balance.
	ClosingCredit int64 `json:"closingCredit"`
}

// TrialBalance is the whole-ledger report: per-account rows + totals + the balance proof.
type TrialBalance struct {
	// From is the posting time the window opens at, as it was asked for. Absent
	// means the report runs from the beginning of the ledger.
	From string `json:"from,omitempty"`
	// To is the posting time the window closes at, inclusive. Absent means "up to
	// now" — every posting the ledger holds.
	To string `json:"to,omitempty"`
	// Rows are the accounts that MOVED in one of the windows. An account that never
	// moved is omitted rather than listed at zero, so this is shorter than the chart.
	Rows []TrialBalanceRow `json:"rows"`
	// TotalDebit is the sum of every row's CLOSING debit column, in cents.
	TotalDebit int64 `json:"totalDebit"`
	// TotalCredit is the sum of every row's closing credit column, in cents.
	TotalCredit int64 `json:"totalCredit"`
	// Balanced is the proof this report exists to give: whether total debits equal
	// total credits. It is computed from the rows above, never assumed, and false
	// means the ledger itself is broken rather than that the report is wrong.
	Balanced bool `json:"balanced"`
}

// trialBalance builds the report from the store over an optional [from, to] window. It
// reads opening (movement strictly before `from`) and closing (movement up to and
// including `to`) once each, derives period = closing − opening, and places every net on
// its natural column by sign. Accounts with no movement in any window are omitted (a
// trial balance lists only accounts that moved).
func trialBalance(ctx context.Context, s *store, from, to string) (TrialBalance, error) {
	var opening sums
	var err error
	if from != "" {
		if opening, err = s.sums(ctx, "", from); err != nil { // strictly before `from`
			return TrialBalance{}, err
		}
	} else {
		opening = sums{}
	}
	closing, err := s.sums(ctx, "", to) // up to and including `to` ("" = all time)
	if err != nil {
		return TrialBalance{}, err
	}

	tb := TrialBalance{From: from, To: to, Rows: []TrialBalanceRow{}}
	for _, a := range chartOfAccounts {
		o := opening[a.Number]
		c := closing[a.Number]
		if o == [2]int64{} && c == [2]int64{} {
			continue
		}
		row := TrialBalanceRow{Account: a.Number, Name: a.Name, Type: a.Type}
		placeNet(&row.OpeningDebit, &row.OpeningCredit, o[0]-o[1])
		placeNet(&row.Debit, &row.Credit, (c[0]-o[0])-(c[1]-o[1]))
		placeNet(&row.ClosingDebit, &row.ClosingCredit, c[0]-c[1])
		tb.Rows = append(tb.Rows, row)
		tb.TotalDebit += row.ClosingDebit
		tb.TotalCredit += row.ClosingCredit
	}
	tb.Balanced = tb.TotalDebit == tb.TotalCredit
	return tb, nil
}

// placeNet puts a signed net (debit − credit) onto its natural column by SIGN, so a report
// row never shows a negative amount: a positive net is a debit balance, a negative net a
// credit balance. Placement is independent of the account's normal side — for every net,
// normal-side placement collapses to exactly this sign rule, and placing by sign faithfully
// shows a contra-balance instead of forcing it onto the normal column. The magnitude always
// lands on ONE column; the other stays zero.
func placeNet(debitCol, creditCol *int64, net int64) {
	if net >= 0 {
		*debitCol = net
	} else {
		*creditCol = -net
	}
}
