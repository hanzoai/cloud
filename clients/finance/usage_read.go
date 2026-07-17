package finance

import "context"

// UsageRow is one recorded usage debit — the READ twin of RecordUsage. The SAME
// wallet→revenue posting a metered call wrote is read back here, so the usage a
// customer SEES is exactly what drained their wallet. Cents is the debit magnitude
// (USD minor units); Model is the metered-unit label the debit carried (Entry.Memo);
// CreatedAt is unix seconds.
type UsageRow struct {
	ID        string `json:"id"`
	Cents     int64  `json:"cents"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"createdAt"`
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
			Model:     e.Memo,
			CreatedAt: e.CreatedAt,
		})
	}
	return rows, nil
}
