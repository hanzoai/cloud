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
	Account       string      `json:"account"`
	Name          string      `json:"name"`
	Type          AccountType `json:"type"`
	OpeningDebit  int64       `json:"openingDebit"`
	OpeningCredit int64       `json:"openingCredit"`
	Debit         int64       `json:"debit"`  // period movement
	Credit        int64       `json:"credit"` // period movement
	ClosingDebit  int64       `json:"closingDebit"`
	ClosingCredit int64       `json:"closingCredit"`
}

// TrialBalance is the whole-ledger report: per-account rows + totals + the balance proof.
type TrialBalance struct {
	From        string            `json:"from,omitempty"`
	To          string            `json:"to,omitempty"`
	Rows        []TrialBalanceRow `json:"rows"`
	TotalDebit  int64             `json:"totalDebit"`
	TotalCredit int64             `json:"totalCredit"`
	Balanced    bool              `json:"balanced"`
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
