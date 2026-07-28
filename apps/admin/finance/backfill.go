package finance

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	ledger "github.com/hanzoai/cloud/apps/finance"
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
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   *Backfilled `json:"data"`
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
	c, err := core.Admit(ctx)
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
	// As(c) delegates the SuperAdmin core.Admit just validated; For(org) names the tenant
	// being migrated, which is the org whose books the callee then scopes to.
	out, err := cloud.Dial("commerce").As(c).For(org).Call(ctx, "finance.balance",
		cloud.PutBalanceReq(org, "usd"))
	if err != nil {
		return &BackfillOut{Status: core.Err, Msg: "read commerce balance: " + err.Error()}, nil
	}
	balanceCents, err := cloud.I64(out)
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
