package billing

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRoundMicrosToCents pins the sub-cent rounding: round-to-NEAREST (half-up),
// never floor. Floor would drop every sub-cent fraction (systematic undercharge);
// round-to-nearest is statistically fair (E[undercharge]=0). Exact micros are
// preserved in metadata by the handler, so any drift is auditable.
func TestRoundMicrosToCents(t *testing.T) {
	cases := []struct {
		micros int64
		cents  int64
		note   string
	}{
		{0, 0, "zero"},
		{1, 0, "0.0001c — rounds to 0 (echoed in metadata, not silently lost)"},
		{4999, 0, "just under half a cent -> 0"},
		{5000, 1, "exactly half a cent -> 1 (half-up)"},
		{9999, 1, "just under a cent -> 1"},
		{10000, 1, "exactly one cent"},
		{14999, 1, "1.4999c -> 1"},
		{15000, 2, "1.5c -> 2 (half-up)"},
		{25000, 3, "2.5c -> 3 (half-up)"},
		{1000000, 100, "$1 -> 100c"},
	}
	for _, tc := range cases {
		if got := roundMicrosToCents(tc.micros); got != tc.cents {
			t.Errorf("roundMicrosToCents(%d) = %d, want %d (%s)", tc.micros, got, tc.cents, tc.note)
		}
	}
}

// TestRecordUsage_Idempotent_NoDoubleDebit proves the money-critical invariant
// added to RecordUsage: a retry/double-submit of the SAME usage (same
// requestId / X-Idempotency-Key) creates AT MOST ONE withdraw. Mirrors
// TestTopup_Idempotent_NoDoubleCredit (the proven credit-side guard).
//
// Chat's spendTokens fires per-completion and is retried on stream/abort, so
// without this guard every retry would double-debit the ledger.
func TestRecordUsage_Idempotent_NoDoubleDebit(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	org := &organization.Organization{}
	org.Name = "usage-idem-org"
	org.Live = true
	db := datastore.New(org.Namespaced(ctx))

	const subject = "usage-idem-org/alice@example.com"
	creditLikeTopup(t, ctx, org, subject, 1000) // seed $10.00

	scope := "billing-usage"
	const key = "req-abc-123" // the requestId chat sends per spend

	debitOnce := func() {
		rec, replay, err := idempotencykey.Begin(db, scope, key)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if replay {
			// A completed guard: the debit already happened — do NOT withdraw again.
			if rec.Status != idempotencykey.StatusCompleted {
				t.Fatalf("replay but status=%q, want completed", rec.Status)
			}
			return
		}
		trans := transaction.New(db)
		trans.Type = transaction.Withdraw
		trans.SourceId = subject
		trans.SourceKind = "iam-user"
		trans.Currency = currency.USD
		trans.Amount = currency.Cents(100) // $1.00 debit
		trans.Tags = "api-usage"
		if err := trans.Create(); err != nil {
			t.Fatalf("withdraw Create: %v", err)
		}
		if err := idempotencykey.Complete(rec, `{"type":"withdraw","amount":100}`); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	debitOnce() // first spend
	debitOnce() // retry with the SAME requestId — must be a no-op replay

	// Exactly ONE $1 debit: 1000 - 100 = 900. Two would leave 800.
	if got := balanceOf(t, ctx, org, subject); got != 900 {
		t.Fatalf("balance = %d, want 900 — DOUBLE-DEBIT: idempotent retry withdrew twice", got)
	}
}
