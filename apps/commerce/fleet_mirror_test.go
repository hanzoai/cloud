// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"
	"time"

	cloudds "github.com/hanzoai/cloud/datastore"
)

func TestFleetMirrorCanonicalAnalyticsMappers(t *testing.T) {
	now := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)

	// Deterministic entry_id guarantees replay safety.
	id1 := deterministicLedgerEntryID("Deposit", "acme", "tx_123", "ref_1")
	id2 := deterministicLedgerEntryID("deposit", "acme", "tx_123", "ref_1")
	if id1 != id2 || id1 != "deposit:acme:tx_123" {
		t.Fatalf("deterministicLedgerEntryID mismatch: %q vs %q", id1, id2)
	}
	idRef := deterministicLedgerEntryID("refund", "acme", "", "inv_999")
	if idRef != "refund:acme:ref:inv_999" {
		t.Fatalf("ref fallback entry_id = %q, want refund:acme:ref:inv_999", idRef)
	}

	// Org balance dual-write mapper stamps strictly monotonic version & normalized currency.
	b1 := analyticsOrgBalanceFrom(cloudds.FleetOrgBook{
		Org:         "acme",
		Books:       true,
		CashCents:   12500,
		CreditCents: 3000,
		LimitCents:  50000,
		OwedCents:   0,
		UpdatedAt:   now,
	}, "USD", 0)
	b2 := analyticsOrgBalanceFrom(cloudds.FleetOrgBook{
		Org:       "acme",
		CashCents: 12000,
		UpdatedAt: now,
	}, "", 0)
	if b1.Currency != "usd" || b2.Currency != "usd" || b1.CashMinor != 12500 || b1.CreditMinor != 3000 || !b1.Books {
		t.Fatalf("unexpected org balance mapping: %+v", b1)
	}
	if b2.Version <= b1.Version {
		t.Fatalf("expected strictly monotonic version: b2=%d <= b1=%d", b2.Version, b1.Version)
	}

	// Subscription dual-write mapper preserves minor MRR, currency, and test/cancel flags.
	sub := analyticsSubscriptionFrom(cloudds.FleetSubscription{
		Org:               "acme",
		ID:                "sub_1",
		UserID:            "u1",
		PlanID:            "max-20x",
		PlanName:          "Max 20x",
		Status:            "active",
		MRRCents:          20000,
		Seats:             5,
		Interval:          "month",
		Provider:          "square",
		Settled:           "card",
		Test:              false,
		CancelAtPeriodEnd: true,
		PayloadJSON:       "{}",
		UpdatedAt:         now,
	}, "EUR", 0)
	if sub.Currency != "eur" || sub.MRRMinor != 20000 || sub.Seats != 5 || !sub.CancelAtPeriodEnd || sub.Version == 0 {
		t.Fatalf("unexpected subscription mapping: %+v", sub)
	}

	// Invoice dual-write mapper preserves amount/paid minor units and currency.
	inv := analyticsInvoiceFrom(cloudds.FleetInvoice{
		Org:            "acme",
		ID:             "inv_1",
		Number:         "INV-42",
		UserID:         "u1",
		SubscriptionID: "sub_1",
		Status:         "paid",
		AmountCents:    20000,
		PaidCents:      20000,
		Currency:       "GBP",
		IssuedAt:       "2026-11-01T00:00:00Z",
		PaidAt:         "2026-11-01T00:05:00Z",
		PayloadJSON:    "{}",
		UpdatedAt:      now,
	}, 0)
	if inv.Currency != "gbp" || inv.AmountMinor != 20000 || inv.PaidMinor != 20000 || inv.Version == 0 {
		t.Fatalf("unexpected invoice mapping: %+v", inv)
	}
}

func TestFleetMirrorDebounceCoalescesBursts(t *testing.T) {
	key := "book:test-debounce-org-" + t.Name()
	if !allowFleetMirror(key) {
		t.Fatal("first allowFleetMirror should return true")
	}
	if allowFleetMirror(key) {
		t.Fatal("immediate second allowFleetMirror should be coalesced (false)")
	}
}
