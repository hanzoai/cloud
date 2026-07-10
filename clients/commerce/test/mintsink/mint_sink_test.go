// Copyright © 2026 Hanzo AI. MIT License.

// Package mintsink_test proves the STRUCTURAL money-mint invariant at the ledger
// layer — the datastore write sink itself refuses to persist an unauthorized
// spendable mint — WITHOUT any HTTP handler or route in the picture. This is the
// proof that the fix is closed-form: a mint cannot be written regardless of which
// route (or none) reaches transaction.Create(), because enforcement lives at
// datastore.Put via mintauth.Enforce.
package mintsink_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// build makes an unsaved transaction of the given type/destination-kind on ctx.
// A $1,000,000 amount so a leak would be unmistakable.
func build(ctx context.Context, typ transaction.Type, destKind, destID string) *transaction.Transaction {
	tr := transaction.New(datastore.New(ctx))
	tr.Type = typ
	tr.DestinationId = destID
	tr.DestinationKind = destKind
	tr.SourceId = "attacker-source"
	tr.SourceKind = "attacker-source"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(100_000_000)
	return tr
}

func gated() context.Context     { return mintauth.WithGate(context.Background()) }
func gatedAuth() context.Context { return mintauth.WithAuthorized(mintauth.WithGate(context.Background())) }

// TestSink_RefusesUnauthorizedIAMUserDeposit is THE structural proof: a Deposit
// crediting the gateway-spendable IAM-user wallet, on a mint-gated context with
// NO authorization, is REFUSED at trans.Create() — the ledger sink, not a route.
func TestSink_RefusesUnauthorizedIAMUserDeposit(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	tr := build(gated(), transaction.Deposit, transaction.IAMUserKind, "victim/alice")
	err := tr.Create()
	if !errors.Is(err, mintauth.ErrNotAuthorized) {
		t.Fatalf("gated+unauthorized $1M iam-user deposit: Create()=%v, want ErrNotAuthorized — the mint MUST be refused at the sink", err)
	}
}

// TestSink_RefusesUnauthorizedIAMUserTransfer proves the forged-Transfer variant
// (credit INTO the gateway wallet from an attacker-named source) is also refused.
func TestSink_RefusesUnauthorizedIAMUserTransfer(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	tr := build(gated(), transaction.Transfer, transaction.IAMUserKind, "victim/bob")
	if err := tr.Create(); !errors.Is(err, mintauth.ErrNotAuthorized) {
		t.Fatalf("gated+unauthorized iam-user transfer: Create()=%v, want ErrNotAuthorized", err)
	}
}

// TestSink_AllowsAuthorizedIAMUserDeposit — control: the SAME mint with
// authorization (a proven mint principal / settled payment) is written.
func TestSink_AllowsAuthorizedIAMUserDeposit(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	tr := build(gatedAuth(), transaction.Deposit, transaction.IAMUserKind, "cust/carol")
	if err := tr.Create(); err != nil {
		t.Fatalf("gated+AUTHORIZED iam-user deposit: Create()=%v, want success", err)
	}
}

// TestSink_AllowsUngatedMint — control: an UNGATED context (a background cron, a
// migration, an internal call that never crossed the HTTP boundary) mints freely,
// so no internal/test flow is broken by the invariant.
func TestSink_AllowsUngatedMint(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	tr := build(context.Background(), transaction.Deposit, transaction.IAMUserKind, "cron/dave")
	if err := tr.Create(); err != nil {
		t.Fatalf("ungated iam-user deposit: Create()=%v, want success (backward-compatible)", err)
	}
}

// TestSink_AllowsNonMintWrites — control: a Withdraw (debit) and a Deposit to a
// NON-gateway wallet ("user") are not spendable mints, so a gated-unauthorized
// context writes them fine (no over-blocking of legit ledger activity).
func TestSink_AllowsNonMintWrites(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()

	withdraw := build(gated(), transaction.Withdraw, transaction.IAMUserKind, "cust/erin")
	if err := withdraw.Create(); err != nil {
		t.Fatalf("gated withdraw (a debit, not a mint): Create()=%v, want success", err)
	}

	legacyWallet := build(gated(), transaction.Deposit, "user", "cust/frank")
	if err := legacyWallet.Create(); err != nil {
		t.Fatalf("gated deposit to legacy non-gateway 'user' wallet: Create()=%v, want success (out of scope)", err)
	}
}
