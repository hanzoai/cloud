package billing

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// H1 (Red): the ledger test bucket MUST follow the charge environment
// (org.TestMode), not org.Live alone. These tests credit a real Deposit through
// the actual transaction ledger exactly as the topup path does (trans.Test =
// org.TestMode()) and prove credit-bucket == read-bucket == charge-env, and that
// the OPPOSITE bucket stays empty — closing the sandbox-charge → live-credit
// (free money) and production-charge → test-credit (mislabeled revenue) holes.

// depositLikeTopup mirrors api/billing/topup.go chargeAndCredit: the Deposit's
// Test bucket is the org's resolved TestMode.
func depositLikeTopup(db *datastore.Datastore, org *organization.Organization, user string, cents int64) {
	tr := transaction.New(db)
	tr.Type = transaction.Deposit
	tr.DestinationId = user
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.Test = org.TestMode() // the fix under test
	tr.MustCreate()
}

func bucketBalance(t *testing.T, ctx ae.Context, user string, test bool) currency.Cents {
	t.Helper()
	datas, err := txutil.GetTransactionsByCurrency(ctx, user, "iam-user", currency.USD, test)
	if err != nil {
		t.Fatalf("GetTransactionsByCurrency(test=%v): %v", test, err)
	}
	if d, ok := datas.Data[currency.USD]; ok && d != nil {
		return d.Balance
	}
	return 0
}

// Matrix A — the FREE-MONEY case: SQUARE_ENVIRONMENT=sandbox (a testnet/devnet
// deploy this feature enables) with a LIVE org. A sandbox charge must credit the
// TEST bucket; the LIVE (spendable) bucket must stay empty.
func TestH1_SandboxEnvLiveOrg_CreditsTestBucketNotLive(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "sandbox")
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	org := &organization.Organization{}
	org.Live = true // org says "live", but the DEPLOYMENT is sandbox

	if !org.TestMode() {
		t.Fatal("precondition: sandbox deploy must put a live org in test mode")
	}

	const user = "h1-matrixA-user"
	depositLikeTopup(db, org, user, 500)

	if got := bucketBalance(t, ctx, user, true); got != 500 {
		t.Fatalf("TEST bucket = %d, want 500 (sandbox charge must credit the test bucket)", got)
	}
	if got := bucketBalance(t, ctx, user, false); got != 0 {
		t.Fatalf("LIVE bucket = %d, want 0 — FREE-MONEY hole: a sandbox charge credited the spendable live balance", got)
	}
}

// Matrix B — production deploy with a test-mode org: the charge is real, so it
// must book the LIVE bucket (real revenue), not the test bucket.
func TestH1_ProductionEnvTestOrg_CreditsLiveBucketNotTest(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	org := &organization.Organization{}
	org.Live = false // org in "test mode", but the DEPLOYMENT is production

	if org.TestMode() {
		t.Fatal("precondition: production deploy must put a test-mode org in live mode")
	}

	const user = "h1-matrixB-user"
	depositLikeTopup(db, org, user, 700)

	if got := bucketBalance(t, ctx, user, false); got != 700 {
		t.Fatalf("LIVE bucket = %d, want 700 (production charge books real revenue)", got)
	}
	if got := bucketBalance(t, ctx, user, true); got != 0 {
		t.Fatalf("TEST bucket = %d, want 0 — production revenue mislabeled as test (under-pays OSS payout)", got)
	}
}
