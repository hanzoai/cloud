package finance

import (
	"context"

	"github.com/hanzoai/cloud/apps/money"
)

// UsageRow is one recorded usage debit — the READ twin of RecordUsage. The SAME
// wallet→revenue posting a metered call wrote is read back here, so the usage a
// customer SEES is exactly what drained their wallet. Cents is the debit magnitude
// (USD minor units); Model is the metered-unit label the debit carried (Entry.Memo);
// CreatedAt is unix seconds.
type UsageRow struct {
	ID    string `json:"id"`
	Cents int64  `json:"cents"`
	// Amount is the debit exactly as the ledger holds it — the same 18-decimal
	// value the wallet→revenue posting moved. Cents beside it is the ROUNDING of
	// this value, kept for the wires that already carry cents; it is never the
	// source. TxnRow below has said why since it was written: "Flattening to
	// cents here would round away everything below a cent, which is most of what
	// a per-token AI price IS" — and this row, read from the SAME ledger, was
	// flattening. A customer whose usage was a thousand sub-cent calls read back
	// a page of zeros that summed to zero.
	Amount    money.Amount `json:"amount"`
	Model     string       `json:"model"`
	CreatedAt int64        `json:"createdAt"`
}

// ListUsage returns org's recorded usage debits, most-recent-first, up to limit
// (limit <= 0 lists all). It reads the org's OWN finance file — the file IS the
// tenant boundary, so this can only ever return the caller's org's usage — and keeps
// ONLY the usage-debit entries (a deposit/grant is not usage).
//
// It is the CO-RESIDENT read the customer billing surface uses INSTEAD of the S2S
// HTTP hop: co-resident, commerce's own /v1/billing/usage route is not compiled into
// this binary, so proxying that path self-dispatches straight back into the customer
// handler. Reading the ledger here is the same move balance() already makes, so the
// usage view can never self-answer "sign in to view billing".
func (f *ledgerFinance) ListUsage(ctx context.Context, org string, limit int) ([]UsageRow, error) {
	store, err := f.storeFor(org, false)
	if err != nil {
		return nil, err
	}
	entries, err := store.Entries(ctx, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]UsageRow, 0, len(entries))
	for _, e := range entries {
		if e.Kind != kindUsage {
			continue // deposits/grants are credits, not usage
		}
		rows = append(rows, UsageRow{
			ID:        e.ID,
			Cents:     e.Amount.Cents(),
			Amount:    e.Amount,
			Model:     e.Memo,
			CreatedAt: e.CreatedAt,
		})
	}
	return rows, nil
}

// TxnRow is one ledger entry as the billing surface reads it: every kind, not only
// the usage debits ListUsage keeps. Credits, grants and debits are all transactions
// to a customer looking at their account, and the three finance pages
// (credits/usage/ledger) are three projections of THIS one list.
//
// Amount stays the ledger's own money.Amount — 18-decimal exact, the same integer
// an on-chain balance holds — because this is where that value lives. Flattening to
// cents here would round away everything below a cent, which is most of what a
// per-token AI price IS.
type TxnRow struct {
	ID        string
	Kind      string
	Ref       string
	Memo      string
	Amount    money.Amount
	CreatedAt int64
}

// ListEntries returns org's ledger entries, most-recent-first, up to limit
// (limit <= 0 lists all). Same read as ListUsage and the same tenant boundary — the
// org's own file — but unfiltered: the caller decides which kinds its page shows.
func (f *ledgerFinance) ListEntries(ctx context.Context, org string, limit int) ([]TxnRow, error) {
	store, err := f.storeFor(org, false)
	if err != nil {
		return nil, err
	}
	entries, err := store.Entries(ctx, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]TxnRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, TxnRow{
			ID:        e.ID,
			Kind:      e.Kind,
			Ref:       e.Ref,
			Memo:      e.Memo,
			Amount:    e.Amount,
			CreatedAt: e.CreatedAt,
		})
	}
	return rows, nil
}
