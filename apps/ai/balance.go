package ai

import (
	"cmp"
	"context"
	"fmt"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

// balance is what a priced call is admitted against: the payer's whole spendable
// balance, cash plus credit, in whole cents, floored. It is finance.Funds.Total, read
// in the process that owns the ledger when this is that process and over the plane
// otherwise; nothing here recomputes it.
//
// Credit counts: a grant is money its org may spend. The debit draws cash first, then
// credit (finance draw), so what a customer paid is spent before anything minted.
//
// ROUND DOWN: a gate that rounded up would admit a call the balance cannot cover, and
// the debit that follows is exact. A read that fails is an error, never zero.
func balance(ctx context.Context, subject, namespace, currency string) (int64, error) {
	if f, ok := finance.Current().(finance.Funder); ok {
		funds, err := f.Funds(ctx, namespace, subject, network.SandboxOnly())
		if err != nil {
			return 0, err
		}
		return client.Amount(funds.Total().Unwrap()).FloorMinor()
	}
	bal, err := commercepeer.FinanceBalance(cloud.For(ctx, namespace), &client.BalanceIn{Subject: subject, Currency: cmp.Or(currency, "usd")})
	if err != nil {
		return 0, fmt.Errorf("plane balance read: %w", err)
	}
	if bal == nil {
		return 0, fmt.Errorf("plane balance read: commerce answered nothing")
	}
	cents, err := bal.Amount.FloorMinor()
	if err != nil {
		return 0, fmt.Errorf("plane balance read: %w", err)
	}
	return cents, nil
}
