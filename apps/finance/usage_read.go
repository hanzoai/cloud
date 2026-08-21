package finance

import (
	"context"

	"github.com/hanzoai/cloud/money"
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
// test selects the SANDBOX books, the same selector every other read on this
// ledger takes (Balance, SumUsageSince). These two list reads arrived later and
// forgot it, so a sandbox caller was silently answered from real money.
func (f *ledgerFinance) ListUsage(ctx context.Context, org string, test bool, limit int) ([]UsageRow, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return nil, err
	}
	entries, err := store.Entries(ctx, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]UsageRow, 0, len(entries))
	for _, e := range entries {
		if ParseKind(e.Kind) != KindUsage {
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
//
// Kind is the ledger's OWN [Kind], not a string: a customer-facing page decides
// whether a row is money in or money out by comparing it, and a reader holding its
// own string literals is how the credits page came to render empty against a ledger
// full of grants.
type TxnRow struct {
	ID        string
	Kind      Kind
	Ref       string
	Memo      string
	Amount    money.Amount
	CreatedAt int64
}

// ListEntries returns subject's ledger entries within org, most-recent-first, up to
// limit (limit <= 0 lists all). Every kind, unfiltered by kind: the caller decides
// which kinds its page shows.
//
// THE ENTRIES ARE THE ONES [ledgerFinance.Balance] COUNTS. A balance is the settled
// sum of one WALLET — walletAcct(subject) — and these are the movements of that same
// wallet, resolved through that same one function, so the running total and the list
// it is made of can never name two accounts. An org whose members all pool resolves
// to the pool wallet either way; where the subject is a person (the shared signup
// org, where account.Payer resolves everyone to <org>/<name>) it is their own.
//
// The wallet is on the ENTRY, not beside it, so there is nothing to backfill: a
// posting is addressed to an account by construction — a deposit credits
// walletAcct(subject) and a usage debit drains it — so every entry this ledger has
// ever written already names the wallet it moved.
//
// The org's file is still the tenant boundary — nothing here can reach another org's
// books — and an empty subject reads every wallet in it, which is the org-wide read
// the revenue ingest takes.
func (f *ledgerFinance) ListEntries(ctx context.Context, org, subject string, test bool, limit int) ([]TxnRow, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return nil, err
	}
	acct := ""
	if subject != "" {
		acct = walletAcct(subject)
	}
	entries, err := store.EntriesOn(ctx, acct, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]TxnRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, TxnRow{
			ID:        e.ID,
			Kind:      ParseKind(e.Kind),
			Ref:       e.Ref,
			Memo:      e.Memo,
			Amount:    e.Amount,
			CreatedAt: e.CreatedAt,
		})
	}
	return rows, nil
}
