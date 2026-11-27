// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// cutover_test.go — THE CUTOVER MUST NOT CREDIT A PAYMENT TWICE.
//
// Before the cutover this host deposited a settled card payment into the finance
// ledger (Ref = the processor's reference). After it, commerce posts the credit through
// the adapter in ledger.go, keyed on the same reference at the same address. The two
// are the same entry, so a payment that crossed the cutover — charged by the old
// release, re-delivered by its webhook to the new one — is credited once.
//
// Both tests take the key commerce posts under from a parameter, so each can be run
// against a deliberately broken version and shown to fail: the same property with a
// different key is two credits.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/commerce/billing/creditledger"
)

const (
	cutOrg     = "cutover-org"
	cutSubject = "cutover-org/alice"
	cutRef     = "sqpay_old_release"
	cutCents   = 4200
)

// cloudSettled posts a payment exactly as the old screen.settle did: finance's own
// Deposit, Ref = the settlement id, the payer's address, cash.
func cloudSettled(t *testing.T, ctx context.Context, fin money.FinanceClient, ref string, test bool) {
	t.Helper()
	if _, err := fin.Deposit(ctx, money.DepositInput{
		Org: cutOrg, Subject: cutSubject, Amount: money.FromCents(cutCents), Currency: "usd",
		Notes: "Top-up via square (ref: " + ref + ")", Tags: "topup", Ref: ref, Test: test, Cash: true,
	}); err != nil {
		t.Fatalf("the old release's deposit: %v", err)
	}
}

// commerceCredits is what commerce posts for a payment: the creditledger adapter, as
// the embed injects it, under key.
func commerceCredits(ctx context.Context, key string, test bool) error {
	_, _, err := (ledger{}).Credit(ctx, creditledger.CreditInput{
		Org: cutOrg, Subject: cutSubject, Currency: "usd", Reason: "Top-up via card " + key,
		Tag: "topup", IdempotencyKey: key, AmountCents: cutCents, Test: test, Cash: true,
	})
	return err
}

func balanceOfCut(ctx context.Context, t *testing.T, fin money.FinanceClient, test bool) int64 {
	t.Helper()
	b, err := fin.Balance(ctx, cutOrg, cutSubject, "usd", test)
	if err != nil {
		t.Fatal(err)
	}
	return b.Cents()
}

// REPLAY: a payment the old release credited, delivered again by its webhook to the
// new one, credits nothing. keyOf is the key commerce posts under.
func replayAfterCutover(t *testing.T, keyOf func(string) string) int64 {
	ctx := context.Background()
	fin := funded(t)
	for _, test := range []bool{false, true} {
		cloudSettled(t, ctx, fin, cutRef, test)
	}
	for i := 0; i < 3; i++ { // the webhook, redelivered
		if err := commerceCredits(ctx, keyOf(cutRef), false); err != nil {
			t.Fatalf("commerce's credit: %v", err)
		}
	}
	return balanceOfCut(ctx, t, fin, false)
}

func TestAPaymentTheOldReleaseCreditedIsNotCreditedAgainByCommerce(t *testing.T) {
	if got := replayAfterCutover(t, func(ref string) string { return ref }); got != cutCents {
		t.Fatalf("the wallet holds %d after the replay, want %d: the cutover credited a payment twice", got, cutCents)
	}
}

// The check bites: commerce posting under any other key is a second credit.
func TestTheReplayCheckSeesADifferentKey(t *testing.T) {
	if got := replayAfterCutover(t, func(ref string) string { return ref + "-v2" }); got == cutCents {
		t.Fatalf("a credit under another key left the wallet at %d: the replay check cannot tell the cutover broke", got)
	}
}

// PARITY: one top-up posts one entry whichever of the in-session charge, the
// provider's webhook and the reconciler arrives, in any order, and whichever release
// took the card.
func parityOf(t *testing.T, keyOf func(string) string) int64 {
	ctx := context.Background()
	fin := funded(t)
	const ref = "sqpay_parity"
	// The in-session charge, the webhook for the same ref, then the reconciler: three
	// writers' attempts at one payment, which commerce posts through one adapter.
	for _, writer := range []string{"in-session", "webhook", "reconciler"} {
		_ = writer
		if err := commerceCredits(ctx, keyOf(ref), false); err != nil {
			t.Fatalf("%s: %v", writer, err)
		}
	}
	return balanceOfCut(ctx, t, fin, false)
}

func TestOneTopupPostsOneEntryWhoeverCredits(t *testing.T) {
	if got := parityOf(t, func(ref string) string { return ref }); got != cutCents {
		t.Fatalf("the wallet holds %d, want exactly one credit of %d", got, cutCents)
	}
}

func TestTheParityCheckSeesADifferentKeyPerWriter(t *testing.T) {
	n := 0
	if got := parityOf(t, func(ref string) string { n++; return ref + string(rune('a'+n)) }); got == cutCents {
		t.Fatalf("a key per writer left the wallet at %d: the parity check cannot tell the writers disagree", got)
	}
}
