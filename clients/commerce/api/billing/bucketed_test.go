package billing

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// These integration tests exercise the three-bucket split over a REAL datastore
// (the same ledger writes deposit.go/topup.go/usage.go make), proving the split
// the balance endpoints return is derived from real tagged transactions — and
// that a GPU charge is provably barred from credit grants.

// seedCredit inserts a raw deposit transaction for test setup. Named seedCredit
// (not credit) to avoid shadowing the imported billing/credit package at package
// scope — the collision broke the whole api/billing test build.
func seedCredit(db *datastore.Datastore, user string, cents int64, tag string, expires time.Time) {
	tr := transaction.New(db)
	tr.Type = transaction.Deposit
	tr.DestinationId = user
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.Tags = tag
	tr.ExpiresAt = expires
	tr.MustCreate()
}

func spend(db *datastore.Datastore, user string, cents int64, tag string) {
	tr := transaction.New(db)
	tr.Type = transaction.Withdraw
	tr.SourceId = user
	tr.SourceKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.Tags = tag
	tr.MustCreate()
}

// The balance split distinguishes non-cash credit grants from prepaid real money.
func TestBucketedSplit_CreditsVsPrepaid(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	const user = "split-user"
	seedCredit(db, user, 10000, "starter-credit", time.Now().AddDate(1, 0, 0)) // $100 grant
	seedCredit(db, user, 5000, "topup", time.Time{})                           // $50 real money

	s, err := bucketedSplit(ctx, user, currency.USD, false)
	if err != nil {
		t.Fatalf("bucketedSplit: %v", err)
	}
	if s.CreditsRemaining != 10000 {
		t.Errorf("CreditsRemaining = %d, want 10000", s.CreditsRemaining)
	}
	if s.PrepaidBalance != 5000 {
		t.Errorf("PrepaidBalance = %d, want 5000", s.PrepaidBalance)
	}
	// The conflated pre-split balance still reconciles.
	if s.Balance != 15000 {
		t.Errorf("Balance = %d, want 15000", s.Balance)
	}
}

// THE GPU RULE, end-to-end over the real ledger: a gpu-tagged withdrawal reduces
// PREPAID and leaves the credit grant intact — proving GPU spend cannot draw on
// credits.
func TestBucketedSplit_GpuChargeBarredFromCredits(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	const user = "gpu-user"
	seedCredit(db, user, 10000, "starter-credit", time.Now().AddDate(1, 0, 0)) // $100 grant
	seedCredit(db, user, 5000, "topup", time.Time{})                           // $50 prepaid
	spend(db, user, 4000, "gpu-h100")                                      // $40 GPU charge

	s, err := bucketedSplit(ctx, user, currency.USD, false)
	if err != nil {
		t.Fatalf("bucketedSplit: %v", err)
	}
	if s.CreditsRemaining != 10000 {
		t.Errorf("CreditsRemaining = %d, want 10000 — GPU spend must NOT touch credits", s.CreditsRemaining)
	}
	if s.PrepaidBalance != 1000 {
		t.Errorf("PrepaidBalance = %d, want 1000 (5000 prepaid - 4000 GPU)", s.PrepaidBalance)
	}
}

// A credit-only wallet has ZERO prepaid available, so the GPU gate (which reads
// PrepaidAvailable) refuses any GPU charge even though the customer has $100 of
// credit — the exact bar the server enforces before recording a GPU debit.
func TestBucketedSplit_CreditOnlyWallet_NoPrepaidForGpu(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	const user = "credit-only-user"
	seedCredit(db, user, 10000, "starter-credit", time.Now().AddDate(1, 0, 0))

	s, err := bucketedSplit(ctx, user, currency.USD, false)
	if err != nil {
		t.Fatalf("bucketedSplit: %v", err)
	}
	if s.PrepaidAvailable != 0 {
		t.Errorf("PrepaidAvailable = %d, want 0 (credit is not prepaid) — a GPU charge must be refused", s.PrepaidAvailable)
	}
	if s.CreditsRemaining != 10000 {
		t.Errorf("CreditsRemaining = %d, want 10000", s.CreditsRemaining)
	}
}

func TestGetCardOnFile(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	const user = "card-user"
	// No card yet.
	if getCardOnFile(db, user).OnFile {
		t.Fatal("OnFile = true with no payment method")
	}

	pm := paymentmethod.New(db)
	pm.CustomerId = user
	pm.Type = "card"
	pm.IsDefault = true
	pm.ProviderRef = "sq-card-123"
	pm.Card = &paymentmethod.CardDetails{Brand: "visa", Last4: "4242"}
	pm.MustCreate()

	got := getCardOnFile(db, user)
	if !got.OnFile {
		t.Fatal("OnFile = false after adding a chargeable card")
	}
	if got.Brand != "visa" || got.Last4 != "4242" {
		t.Errorf("card = %s/%s, want visa/4242", got.Brand, got.Last4)
	}
}
