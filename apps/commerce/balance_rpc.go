// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	financeclient "github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/money"
	"github.com/zap-proto/zip"
)

// The prepaid ledger is a per-org SQLite store with ONE writer, so exactly one
// process may open it — and that process is the one that mounts commerce, because
// wireFinance builds the ledger only when commerce is enabled. Every other app
// therefore has no ledger to read, which is not a configuration gap to fill in but
// the single-writer property working as intended.
//
// So the ledger is asked, not opened. These are the ops that answer, on the
// internal plane (ZAP on this app's canonical unix socket), and they are why
// billing can report a balance from a process that must never touch the file.
//
// They stay deliberately dumb: the caller resolves WHOSE balance it wants and
// these read it. Subject resolution needs the request — the billing account
// claim, the user name the edge minted — and pushing that here would mean a
// second copy of principal.Subject deriving a payer from facts it does not
// carry. One resolver, at the edge that has the request.

// callerOrg is the ONE tenancy rule these ops share: the org comes from the
// CALLER — the gateway's assertion, or what a background job stated once and
// explicitly — and never from an argument. A caller that could name its own org
// would be naming another tenant's books, and none of these inputs can express
// one.
func callerOrg(ctx context.Context, op string) (string, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return "", zip.ErrForbidden(op + ": no org on the call")
	}
	return org, nil
}

// books is the other rule they share. This process mounts commerce, so the
// ledger is built before routes are served: nil here means the boot order
// changed, and answering zero would report every account as broke.
func books(op string) (financeclient.Client, error) {
	fin := financeclient.Current()
	if fin == nil {
		return nil, fmt.Errorf("%s: no ledger in the process that owns it", op)
	}
	return fin, nil
}

// exposeBalance publishes the ledger read. Mount calls it.
func exposeBalance() {
	zip.Post[plane.BalanceIn, plane.Balance](cloud.Plane(), "/finance/balance",
		func(ctx context.Context, in *plane.BalanceIn) (*plane.Balance, error) {
			org, err := callerOrg(ctx, "balance")
			if err != nil {
				return nil, err
			}
			// The subject is the caller's to choose, but only within that org — it is
			// a wallet inside the ledger the caller's identity already pinned.
			subject := in.Subject
			if subject == "" {
				subject = org
			}
			currency := in.Currency
			if currency == "" {
				currency = "usd"
			}
			fin, err := books("balance")
			if err != nil {
				return nil, err
			}
			bal, err := fin.Balance(ctx, org, subject, currency, false)
			if err != nil {
				return nil, fmt.Errorf("balance: read %s/%s: %w", org, subject, err)
			}
			// The ledger answered exactly; the plane carries exactly. Flattening to
			// cents here was the console's understatement: every sub-cent tail of
			// the true balance vanished between the one writer and every reader.
			return &plane.Balance{Amount: plane.Amount(bal.Unwrap())}, nil
		},
		zip.WithOperationID(plane.FinanceBalance),
		zip.WithSummary("Spendable prepaid balance"))
}

// The welcome grant, published for the same reason the balance read is: the ledger
// has one writer and it lives here.
//
// StarterGrant is middleware on EVERY app's chain, and it used to bail the moment
// it found no local ledger — which, once apps became their own binaries, is every
// process but this one. So a new account was never funded: it reached tracker or
// billing, the grant looked for a ledger that was one socket away, and returned
// silently. An org that should have started with the welcome credit started broke,
// and the paywall refused it correctly for a reason nobody had chosen.
//
// The idempotency key is the ACCOUNT and nothing else, so asking twice — from two
// processes, after a restart, or concurrently — grants once. That property lives in
// finance, which dedups inside the same transaction as the insert; this op only
// carries the question across.
func exposeStarter() {
	zip.Post[plane.StarterIn, plane.Granted](cloud.Plane(), "/finance/starter",
		func(ctx context.Context, in *plane.StarterIn) (*plane.Granted, error) {
			org, err := callerOrg(ctx, "starter")
			if err != nil {
				return nil, err
			}
			subject := in.Subject
			if subject == "" {
				subject = org
			}
			cents, err := cloud.GrantStarter(ctx, org, subject)
			if err != nil {
				return nil, fmt.Errorf("starter: %w", err)
			}
			return &plane.Granted{Amount: plane.Amount(money.FromUSD(cents))}, nil
		},
		zip.WithOperationID(plane.FinanceStarter),
		zip.WithSummary("Issue the opening credit for an org, once"))
}

// usageReadLimit matches what the co-resident reader asks for, so the page a
// customer sees does not change with which process answered.
const usageReadLimit = 2000

// The usage list, for the same reason as the balance and the grant: one writer,
// and it is here.
//
// It sends ROWS, not a rendered view. The HTTP surface builds its own envelope
// from these — sending the envelope would need the renderer to live with the
// ledger, which is the import cycle that shape implies.
func exposeUsage() {
	zip.Post[struct{}, plane.UsageRows](cloud.Plane(), "/finance/usage",
		func(ctx context.Context, _ *struct{}) (*plane.UsageRows, error) {
			org, err := callerOrg(ctx, "usage")
			if err != nil {
				return nil, err
			}
			fin, err := books("usage")
			if err != nil {
				return nil, err
			}
			lister, ok := fin.(interface {
				ListUsage(context.Context, string, int) ([]financeclient.UsageRow, error)
			})
			if !ok {
				return nil, fmt.Errorf("usage: this ledger does not list usage")
			}
			rows, err := lister.ListUsage(ctx, org, usageReadLimit)
			if err != nil {
				return nil, fmt.Errorf("usage: %w", err)
			}
			out := make([]plane.UsageRow, 0, len(rows))
			for _, r := range rows {
				out = append(out, plane.UsageRow{
					ID: r.ID, Model: r.Model,
					// r.Amount is the ledger's own value; r.Cents is its rounding.
					// Rebuilding an "exact" plane amount FROM the rounding was the
					// sharpest form of the flatten: the wire type promised precision
					// the value had already lost.
					Amount:    plane.Amount(r.Amount.Unwrap()),
					CreatedAt: r.CreatedAt,
				})
			}
			return &plane.UsageRows{Rows: out}, nil
		},
		zip.WithOperationID(plane.FinanceUsage),
		zip.WithSummary("Recorded debits for this org"))
}

// The ledger's entries. Three customer-facing pages read this one list, and all
// three answered 501 from a process that does not hold the ledger.
func exposeTxns() {
	zip.Post[struct{}, plane.Txns](cloud.Plane(), "/finance/txns",
		func(ctx context.Context, _ *struct{}) (*plane.Txns, error) {
			org, err := callerOrg(ctx, "txns")
			if err != nil {
				return nil, err
			}
			fin, err := books("txns")
			if err != nil {
				return nil, err
			}
			lister, ok := fin.(interface {
				ListEntries(context.Context, string, int) ([]financeclient.TxnRow, error)
			})
			if !ok {
				return nil, fmt.Errorf("txns: this ledger does not list entries")
			}
			rows, err := lister.ListEntries(ctx, org, usageReadLimit)
			if err != nil {
				return nil, fmt.Errorf("txns: %w", err)
			}
			out := make([]plane.Txn, 0, len(rows))
			for _, r := range rows {
				out = append(out, plane.Txn{
					ID: r.ID, Kind: r.Kind, Ref: r.Ref, Memo: r.Memo,
					Amount:    plane.Money{Decimal: r.Amount.String(), Currency: "USD"},
					CreatedAt: r.CreatedAt,
				})
			}
			return &plane.Txns{Rows: out}, nil
		},
		zip.WithOperationID(plane.FinanceTxns),
		zip.WithSummary("Ledger entries for this org"))
}
