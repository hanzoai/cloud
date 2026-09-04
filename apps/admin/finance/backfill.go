package finance

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/client"
	ledger "github.com/hanzoai/cloud/finance"
)

// BackfillIn is the POST /v1/admin/finance/backfill input.
type BackfillIn struct {
	// Org is the tenant to migrate. Required — there is no fleet-wide form of this
	// cutover, because each org must be reconciled on its own.
	Org string `json:"org"`
}

// Backfilled is the cutover receipt.
type Backfilled struct {
	// Org is the tenant migrated.
	Org string `json:"org"`
	// MigratedCents is the balance carried across, read from commerce BEFORE the move.
	MigratedCents int64 `json:"migratedCents"`
	// EntryID is the finance ledger entry created, or "" when the balance was
	// non-positive and there was nothing to carry.
	EntryID string `json:"entryId"`
}

// BackfillOut is the POST /v1/admin/finance/backfill envelope.
type BackfillOut struct {
	// Status is "ok" or "error". Error means NOTHING was carried — the commerce balance
	// is read and must be readable before the wallet is written, so an unreadable source
	// refuses the cutover rather than migrating a phantom zero.
	Status string `json:"status"`
	// Msg is why, and is empty on success. It names the half that failed: "read commerce
	// balance: ..." before any money moved, "finance backfill: ..." at the write.
	Msg string `json:"msg"`
	// Data is the receipt. Null exactly when Status is "error". A retry is safe: the
	// deposit carries the fixed ref "backfill:<org>", so the wallet is credited at most
	// once however many times this runs.
	Data *Backfilled `json:"data"`
}

// Backfill carries ONE org's current commerce prepaid balance into the native finance
// wallet — the one-time cutover between the two ledgers.
//
// It is IDEMPOTENT: the deposit uses the fixed ref "backfill:<org>", so re-running it
// credits the wallet at most once. Safe to retry.
//
// The pre-migration balance is read from the CO-RESIDENT commerce ledger, not over HTTP:
// the admin HTTP client dials an unroutable in-process address and would read $0, and a
// phantom zero would silently carry nothing while reporting success. When commerce is
// not co-resident this fails rather than migrating nothing.
//
// Example: {"org":"acme"}
// Response: {"status":"ok","msg":"","data":{"org":"acme","migratedCents":50000,"entryId":"fe_01J"}}
func Backfill(ctx context.Context, in *BackfillIn) (*BackfillOut, error) {
	c, err := core.Change(ctx)
	if err != nil {
		return nil, err
	}
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return &BackfillOut{Status: core.Err, Msg: "org is required"}, nil
	}

	// Pre-migration source of truth: the org's current commerce prepaid balance for the
	// org-pool subject (== the org slug), ASKED of the process that owns the ledger
	// rather than opened here. The per-org ledger has one writer, so importing commerce
	// to read it gave admin the whole commerce graph and still could not open the file;
	// the admin commerce HTTP client dials an unroutable in-proc address and reads $0,
	// which would migrate nothing. A missing socket is an ERROR here — never a phantom
	// zero the cutover would silently carry as "nothing to migrate".
	//
	// As(c, org) delegates the SuperAdmin core.Admit just validated and points it
	// at the tenant being migrated, which is the org whose books the callee scopes
	// to. The admin's own identity still travels whole and the callee re-checks it.
	bal, err := cloud.Ask[client.BalanceIn, client.Balance](cloud.As(c, org), "commerce",
		client.FinanceBalance, &client.BalanceIn{Subject: org, Currency: "usd"})
	if err != nil {
		return &BackfillOut{Status: core.Err, Msg: "read commerce balance: " + err.Error()}, nil
	}
	if bal == nil {
		return &BackfillOut{Status: core.Err, Msg: "read commerce balance: commerce answered nothing"}, nil
	}
	// FLOOR, because Minor() REFUSES a sub-cent amount and this is a migration.
	//
	// The ledger keeps eighteen decimals and per-token charges are routinely finer
	// than a cent, so any org that has spent anything carries a sub-cent tail.
	// Minor() answers "is finer than its minor unit; round explicitly" for exactly
	// those, and this returned that as an error — so the backfill migrated ZERO for
	// every active org while reporting an honest-looking failure. Measured against
	// the live value in balance.go's own comment:
	//
	//   149913.078983985999994361  ->  Minor() ERR, Floor 14991307, Round 14991308
	//
	// Floor is the direction a migration must take: the amount MOVED must never
	// exceed the amount held, or the migration mints the difference. The dust stays
	// behind and settles.
	balanceCents, err := bal.Amount.FloorMinor()
	if err != nil {
		return &BackfillOut{Status: core.Err, Msg: "read commerce balance: " + err.Error()}, nil
	}

	entryID, err := ledger.MigrateOrg(ctx, org, balanceCents)
	if err != nil {
		return &BackfillOut{Status: core.Err, Msg: "finance backfill: " + err.Error()}, nil
	}
	return &BackfillOut{Status: core.OK, Data: &Backfilled{
		Org:           org,
		MigratedCents: balanceCents,
		EntryID:       entryID,
	}}, nil
}
