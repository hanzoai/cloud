package books

// balancesheet.go — the Balance Sheet: a point-in-time statement of what the org OWNS
// (Assets) against what it OWES (Liabilities) and what is left over (Equity), proving the
// fundamental accounting equation Assets = Liabilities + Equity.
//
// WHY IT BALANCES. Every voucher balances (Σdebit = Σcredit), so over the WHOLE ledger the
// signed nets sum to zero. Splitting that identity by root gives
//
//	Assets = Liabilities + Equity + (Income − Expense)
//
// i.e. the period's net income belongs to equity. This ledger has no period-close that
// sweeps P&L into retained earnings, so the sheet folds cumulative net income (Income −
// Expense to date) into equity as a derived RETAINED EARNINGS line. With that line present
// the equation closes exactly — the balance proof (Balanced) is computed, never assumed.
//
// SIGN AT DISPLAY ONLY. Assets are debit-normal (shown as stored net); liabilities and
// equity are credit-normal (net flipped once for display). The ledger is never sign-flipped.

import "context"

// BalanceLine is one line of the sheet, in NATURAL (positive in normal operation) sign. A
// derived line (retained earnings) carries no account number.
type BalanceLine struct {
	Account string      `json:"account,omitempty"`
	Name    string      `json:"name"`
	Type    AccountType `json:"type,omitempty"`
	Amount  int64       `json:"amount"` // cents, display sign
}

// BalanceSheet is the point-in-time statement as of a posting time, with the equation proof.
type BalanceSheet struct {
	AsOf             string        `json:"asOf,omitempty"`
	Assets           []BalanceLine `json:"assets"`
	Liabilities      []BalanceLine `json:"liabilities"`
	Equity           []BalanceLine `json:"equity"`
	TotalAssets      int64         `json:"totalAssets"`
	TotalLiabilities int64         `json:"totalLiabilities"`
	TotalEquity      int64         `json:"totalEquity"`
	Balanced         bool          `json:"balanced"` // TotalAssets == TotalLiabilities + TotalEquity
}

// balanceSheet builds the statement from cumulative movement up to and including asOf
// ("" = all time). It reads per-account sums once, places each Asset/Liability/Equity
// account on its side (credit-nature flipped at display), folds cumulative net income
// (Income − Expense) into equity as retained earnings, and proves the equation.
func balanceSheet(ctx context.Context, s *store, asOf string) (BalanceSheet, error) {
	bal, err := s.sums(ctx, "", asOf) // posting_at <= asOf
	if err != nil {
		return BalanceSheet{}, err
	}
	bs := BalanceSheet{AsOf: asOf, Assets: []BalanceLine{}, Liabilities: []BalanceLine{}, Equity: []BalanceLine{}}
	var retained int64 // cumulative net income = Income − Expense, folded into equity
	for _, a := range chartOfAccounts {
		dc, ok := bal[a.Number]
		if !ok {
			continue
		}
		net := dc[0] - dc[1] // debit − credit
		switch a.Type {
		case Asset:
			bs.Assets = append(bs.Assets, BalanceLine{Account: a.Number, Name: a.Name, Type: a.Type, Amount: net})
			bs.TotalAssets += net
		case Liability:
			bs.Liabilities = append(bs.Liabilities, BalanceLine{Account: a.Number, Name: a.Name, Type: a.Type, Amount: -net})
			bs.TotalLiabilities += -net
		case Equity:
			bs.Equity = append(bs.Equity, BalanceLine{Account: a.Number, Name: a.Name, Type: a.Type, Amount: -net})
			bs.TotalEquity += -net
		case Income, Expense:
			// −net flips income (credit-normal) to +revenue and expense (debit-normal) to
			// −cost, so retained accumulates (Income − Expense) in one uniform step.
			retained += -net
		}
	}
	if retained != 0 {
		bs.Equity = append(bs.Equity, BalanceLine{Name: "Retained earnings", Type: Equity, Amount: retained})
		bs.TotalEquity += retained
	}
	bs.Balanced = bs.TotalAssets == bs.TotalLiabilities+bs.TotalEquity
	return bs, nil
}
