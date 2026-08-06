// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// spend_rpc.go — the org's TOTAL, from the process that owns the ledger.
//
// Six subsystems needed this one number and none of them could get it. Each
// asked commerce for GET /v1/billing/usage/rollup over the commerce transport,
// which — co-resident — dispatched the request back into this binary's own
// router by PATH. That route is registered nowhere here (commerce's own
// api.Route() bundle is behind //go:build cloud and is never compiled in), so
// every one of those reads was a 404 dressed up as an upstream failure:
//
//	referrals   qualify a referrer on their spend       → nobody ever qualified
//	affiliates  accrue commission on referred spend     → nothing ever accrued
//	authors     pay an author against package spend     → nothing ever paid
//	usage       the month-to-date figure on the page    → the page showed zero
//	admin       the per-org money row in every rollup   → the fleet read broke
//
// The answer was in a SQLite file the whole time, in whichever process mounted
// commerce, and it is the same figure the rolling spend cap already reads.

// spendWindow is the default window: the calendar month to date, in UTC. It is
// what "month-to-date consumption" has always meant on the pages that show this
// number, stated once here rather than by each caller computing a cutoff.
func spendWindow(since int64) int64 {
	if since > 0 {
		return since
	}
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}

// exposeSpend publishes the totals read. Mount calls it.
func exposeSpend() {
	zip.Post[plane.SpendIn, plane.Spend](cloud.Plane(), "/finance/spend", planeSpend,
		zip.WithOperationID(plane.FinanceSpend),
		zip.WithSummary("Metered consumption over a window, and the wallet behind it"))
}

// Reads the caller org's metered consumption since a moment, beside the prepaid
// balance it is drawn from. `since` is a unix second; 0 reads the calendar month
// to date, which is what every page showing this figure means by it.
//
// It answers from the LEDGER'S OWN windowed sum — the same source the rolling
// spend cap reads — so a program that qualifies an org on its spend and the gate
// that stops that org from spending cannot disagree about the amount. Deposits
// are not consumption and are excluded by that sum; the balance beside it is the
// wallet's settled figure.
//
// The org is the CALLER'S and can never be named in the input, so one tenant can
// never read another's totals. A missing ledger is an ERROR, never a zero: a
// zero here would qualify nobody, accrue nothing and show every customer a blank
// month, silently — which is exactly what the HTTP read it replaces did.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSpend(ctx context.Context, in *plane.SpendIn) (*plane.Spend, error) {
	org, err := callerOrg(ctx, "spend")
	if err != nil {
		return nil, err
	}
	fin, err := books("spend")
	if err != nil {
		return nil, err
	}
	cents, err := fin.SumUsageSince(ctx, org, false, spendWindow(in.Since))
	if err != nil {
		return nil, fmt.Errorf("spend: sum usage for %s: %w", org, err)
	}
	bal, err := fin.Balance(ctx, org, org, "usd", false)
	if err != nil {
		return nil, fmt.Errorf("spend: balance for %s: %w", org, err)
	}
	return &plane.Spend{
		// The sum arrives from the ledger as a cent figure — that is the
		// precision it has, and money.FromCents says so exactly rather than
		// implying a tail the sum never carried.
		Consumed: plane.Amount(money.FromCents(cents).Unwrap()),
		Balance:  plane.Amount(bal.Unwrap()),
	}, nil
}
