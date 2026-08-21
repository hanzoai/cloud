// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	financeclient "github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/plane"
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
	zip.Post[plane.BalanceIn, plane.Balance](cloud.Plane(), "/finance/balance", planeBalance,
		zip.WithOperationID(plane.FinanceBalance),
		zip.WithSummary("Spendable prepaid balance"))
}

// Reads one subject's spendable prepaid balance out of the ledger this process
// owns, so a process that must never open the file can still report a balance.
//
// The ORG is the caller's — the gateway's assertion, or what a background job
// stated once and explicitly — and can never be named in the input, so a caller
// cannot read another tenant's books. The SUBJECT is the caller's to choose, but
// only within that org: it is a wallet inside the ledger the caller's identity
// already pinned. An empty subject reads the org's own account and an empty
// currency reads usd. A missing ledger is an ERROR, never a zero — answering zero
// from the process that owns the file would report every account as broke.
//
// The amount crosses the plane EXACTLY. Flattening it to cents here was the
// console's understatement: every sub-cent tail of the true balance vanished
// between the one writer and every reader.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeBalance(ctx context.Context, in *plane.BalanceIn) (*plane.Balance, error) {
	org, err := callerOrg(ctx, "balance")
	if err != nil {
		return nil, err
	}
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
	return &plane.Balance{Amount: plane.Amount(bal.Unwrap())}, nil
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
	zip.Post[struct{}, plane.UsageRows](cloud.Plane(), "/finance/usage", planeUsage,
		zip.WithOperationID(plane.FinanceUsage),
		zip.WithSummary("Recorded debits for this org"))
}

// Lists the caller org's recorded usage debits — id, model, exact amount and
// timestamp — most recent first, bounded to the same page size the co-resident
// reader asks for so the page a customer sees does not change with which process
// answered.
//
// It takes NO input at all: the org comes from the caller and there is nothing
// else to name, so one org can never list another's debits. It sends ROWS rather
// than a rendered view — the HTTP surface builds its own envelope from these,
// because sending the envelope would put the renderer next to the ledger, which
// is the import cycle that shape implies. A ledger implementation that cannot
// list usage is an error, not an empty page.
//
// Each amount is the ledger's OWN value, never rebuilt from its cent rounding:
// reconstructing an exact plane amount from the rounding was the sharpest form of
// the flatten, a wire type promising precision the value had already lost.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeUsage(ctx context.Context, _ *struct{}) (*plane.UsageRows, error) {
	org, err := callerOrg(ctx, "usage")
	if err != nil {
		return nil, err
	}
	fin, err := books("usage")
	if err != nil {
		return nil, err
	}
	lister, ok := fin.(interface {
		ListUsage(context.Context, string, bool, int) ([]financeclient.UsageRow, error)
	})
	if !ok {
		return nil, fmt.Errorf("usage: this ledger does not list usage")
	}
	rows, err := lister.ListUsage(ctx, org, false, usageReadLimit)
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	out := make([]plane.UsageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, plane.UsageRow{
			ID: r.ID, Model: r.Model,
			Amount:    plane.Amount(r.Amount.Unwrap()),
			CreatedAt: r.CreatedAt,
		})
	}
	return &plane.UsageRows{Rows: out}, nil
}

// The ledger's entries. Three customer-facing pages read this one list, and all
// three answered 501 from a process that does not hold the ledger.
func exposeTxns() {
	zip.Post[plane.TxnsIn, plane.Txns](cloud.Plane(), "/finance/txns", planeTxns,
		zip.WithOperationID(plane.FinanceTxns),
		zip.WithSummary("Ledger entries for this org"))
}

// Lists the caller org's ledger entries — id, kind, ref, memo, amount and
// timestamp — most recent first and bounded to one page. It is the movement list
// behind the customer-facing transactions, credits and receipts pages, all three
// of which read this one list.
//
// The org is the CALLER'S and cannot be named in the input, so one org can never
// read another's entries. What the input DOES name is which books, whose wallet
// and how many rows: `test` reads the SANDBOX ledger, a physically separate file,
// because a caller that posted test rows into real revenue would have restated the
// company's income with nothing downstream able to tell; `subject` is a wallet
// INSIDE the caller's org — the same key the balance op takes, so the movements
// and the standing total answer for one account — and empty reads every wallet in
// the org; `limit` is a page size and 0 takes the default.
//
// Unlike the usage read the amount crosses as a DECIMAL STRING with its currency
// rather than a bare quantity, because an entry is a movement a customer reads
// rather than a number a gate does arithmetic on. A ledger implementation that
// cannot list entries is an error, not an empty page.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTxns(ctx context.Context, in *plane.TxnsIn) (*plane.Txns, error) {
	org, err := callerOrg(ctx, "txns")
	if err != nil {
		return nil, err
	}
	fin, err := books("txns")
	if err != nil {
		return nil, err
	}
	lister, ok := fin.(interface {
		ListEntries(context.Context, string, string, bool, int) ([]financeclient.TxnRow, error)
	})
	if !ok {
		return nil, fmt.Errorf("txns: this ledger does not list entries")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = usageReadLimit
	}
	rows, err := lister.ListEntries(ctx, org, in.Subject, in.Test, limit)
	if err != nil {
		return nil, fmt.Errorf("txns: %w", err)
	}
	out := make([]plane.Txn, 0, len(rows))
	for _, r := range rows {
		out = append(out, plane.Txn{
			// The kind crosses as the LEDGER'S own spelling, and the reader parses
			// it back with finance.ParseKind. It travels as text because the wire is
			// text; it is never re-spelled here into some other vocabulary, which is
			// how the reader came to classify on strings this ledger never writes.
			ID: r.ID, Kind: string(r.Kind), Ref: r.Ref, Memo: r.Memo,
			Amount:    plane.Money{Decimal: r.Amount.String(), Currency: "USD"},
			CreatedAt: r.CreatedAt,
		})
	}
	return &plane.Txns{Rows: out}, nil
}
