package books

// export.go — the complete FINANCIAL PACKAGE: the four statements a tax preparer or an
// investor needs, assembled from the one ledger in a single read so they are mutually
// consistent — the trial balance (the balance proof), the P&L (period margin), the balance
// sheet (the equation proof), and the GL detail behind them. It composes the existing
// reports; it computes no new numbers, so the package can never disagree with a statement
// pulled on its own.

import (
	"context"
	"time"
)

// FinancialPackage is the export bundle over a [From, To] period. GLLimit rows of the most
// recent GL detail are included as the audit trail behind the statements.
type FinancialPackage struct {
	Org          string       `json:"org"`
	From         string       `json:"from,omitempty"`
	To           string       `json:"to,omitempty"`
	GeneratedAt  string       `json:"generatedAt"`
	TrialBalance TrialBalance `json:"trialBalance"`
	PnL          PnL          `json:"pnl"`
	BalanceSheet BalanceSheet `json:"balanceSheet"`
	GL           []GLRow      `json:"gl"`
}

// financialPackage assembles the package for one org's ledger over (from, to]. The balance
// sheet is struck as of `to` (the period end). GL detail is capped at glLimit newest rows.
func financialPackage(ctx context.Context, s *store, org, from, to string, glLimit int) (FinancialPackage, error) {
	tb, err := trialBalance(ctx, s, from, to)
	if err != nil {
		return FinancialPackage{}, err
	}
	pnl, err := profitAndLoss(ctx, s, from, to)
	if err != nil {
		return FinancialPackage{}, err
	}
	bs, err := balanceSheet(ctx, s, to)
	if err != nil {
		return FinancialPackage{}, err
	}
	gl, err := s.listGL(ctx, glLimit)
	if err != nil {
		return FinancialPackage{}, err
	}
	return FinancialPackage{
		Org:          org,
		From:         from,
		To:           to,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		TrialBalance: tb,
		PnL:          pnl,
		BalanceSheet: bs,
		GL:           gl,
	}, nil
}
