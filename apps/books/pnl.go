package books

// pnl.go — the Profit & Loss (income statement) on an ACCRUAL basis: recognized revenue
// (Income roots) minus matched cost (Expense roots) over a period, so the bottom line is
// real gross-then-net margin, not a cash tally. Revenue is booked at consumption
// (recognition) and COGS is accrued to match it (cogsVoucher), which is exactly what makes
// this statement accrual rather than cash.
//
// ROLL-UP BY ROOT. The chart is flat, so an account's ROOT is its AccountType: every Income
// account rolls into income, every Expense account into expense. SIGN AT DISPLAY ONLY —
// income is credit-normal, so its stored net (debit−credit) is negative and is flipped once
// here for presentation; expense is debit-normal and shown as stored. The ledger itself is
// never sign-flipped; only the report is.

import "context"

// PnLLine is one account's contribution to the statement, shown in its NATURAL (positive
// in normal operation) sign: income as its credit balance, expense as its debit balance.
type PnLLine struct {
	// Account is the chart-of-accounts number this line reports on.
	Account string `json:"account"`
	// Name is that account's human name from the fixed chart.
	Name string `json:"name"`
	// Type is the account's fundamental class, which on this statement is always
	// income or expense — it tells a reader which half of the statement the line
	// came from without re-deriving it from the array it arrived in.
	Type AccountType `json:"type"`
	// Amount is the account's movement over the period in whole cents, in its
	// NATURAL sign: positive when the account behaved normally, for income and
	// expense alike. Income is credit-normal so its stored net is flipped once here
	// for display; the ledger underneath is never sign-flipped. A negative amount
	// therefore means the account ran backwards — a refunded sale, a reversed cost.
	Amount int64 `json:"amount"`
}

// PnL is the period income statement: income lines, expense lines, and the net.
// The period is (From, To] — movement strictly after From, up to and including To —
// matching the trial balance's opening/closing boundary convention (from exclusive).
type PnL struct {
	// From opens the period and is EXCLUSIVE — movement strictly after it, matching
	// the trial balance's opening boundary so the two reports agree on what belongs
	// to a period. Absent means from the beginning of the ledger.
	From string `json:"from,omitempty"`
	// To closes the period and is inclusive. Absent means up to now.
	To string `json:"to,omitempty"`
	// Income is the revenue lines that moved in the period, one per account.
	// Accounts that did not move are omitted rather than listed at zero.
	Income []PnLLine `json:"income"`
	// Expense is the cost lines that moved in the period, one per account.
	Expense []PnLLine `json:"expense"`
	// TotalIncome is revenue RECOGNIZED in the period, in cents — accrual, not cash,
	// so a prepaid top-up is not in it until the credit is consumed.
	TotalIncome int64 `json:"totalIncome"`
	// TotalExpense is cost MATCHED to that revenue, in cents, including accrued
	// infrastructure that has not been billed yet.
	TotalExpense int64 `json:"totalExpense"`
	// NetIncome is totalIncome minus totalExpense, in cents. Negative is a loss.
	NetIncome int64 `json:"netIncome"`
}

// profitAndLoss builds the statement from period movement over (from, to]. It reads the
// per-account debit/credit sums once, rolls each Income/Expense account into its root, and
// flips the credit-nature (income) net to a positive display amount. Accounts with no
// movement in the window are omitted (a P&L lists only what moved).
func profitAndLoss(ctx context.Context, s *store, from, to string) (PnL, error) {
	period, err := s.sums(ctx, from, to) // posting_at > from AND <= to
	if err != nil {
		return PnL{}, err
	}
	p := PnL{From: from, To: to, Income: []PnLLine{}, Expense: []PnLLine{}}
	for _, a := range chartOfAccounts {
		dc, ok := period[a.Number]
		if !ok {
			continue
		}
		net := dc[0] - dc[1] // debit − credit
		switch a.Type {
		case Income:
			amt := -net // credit-normal: flip to positive at display
			p.Income = append(p.Income, PnLLine{Account: a.Number, Name: a.Name, Type: a.Type, Amount: amt})
			p.TotalIncome += amt
		case Expense:
			p.Expense = append(p.Expense, PnLLine{Account: a.Number, Name: a.Name, Type: a.Type, Amount: net})
			p.TotalExpense += net
		}
	}
	p.NetIncome = p.TotalIncome - p.TotalExpense
	return p, nil
}
