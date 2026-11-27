// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFleetInserts(t *testing.T) {
	now := time.Date(2026, 11, 25, 2, 30, 0, 0, time.UTC)

	stmt, args := buildOrgBooksInsert([]FleetOrgBook{
		{Org: "acme", Books: true, CashCents: 1000, CreditCents: 500, MTDMonth: "2026-11"},
		{Org: "beta", Books: false, MTDMonth: "2026-11"},
	}, now)
	if !strings.Contains(stmt, fleetOrgBooksTable) {
		t.Fatalf("org books insert missing table: %s", stmt)
	}
	if len(args) != 2*17 {
		t.Fatalf("org books args = %d, want 34", len(args))
	}
	if args[0] != "acme" || args[1] != uint8(1) || args[17] != "beta" || args[18] != uint8(0) {
		t.Fatalf("unexpected org books args: %#v", args)
	}

	sstmt, sargs := buildSubscriptionsInsert([]FleetSubscription{
		{Org: "acme", ID: "sub_1", Status: "active", MRRCents: 2000, PayloadJSON: `{"id":"sub_1"}`},
	}, now)
	if !strings.Contains(sstmt, fleetSubscriptionsTable) || len(sargs) != 16 {
		t.Fatalf("subscriptions insert malformed: len=%d %s", len(sargs), sstmt)
	}

	istmt, iargs := buildInvoicesInsert([]FleetInvoice{
		{Org: "acme", ID: "inv_1", Status: "paid", AmountCents: 2000, PaidCents: 2000, PayloadJSON: `{"id":"inv_1"}`},
	}, now)
	if !strings.Contains(istmt, fleetInvoicesTable) || len(iargs) != 14 {
		t.Fatalf("invoices insert malformed: len=%d %s", len(iargs), istmt)
	}
}

func TestFleetCoercers(t *testing.T) {
	if got := dsInt64(uint8(1)); got != 1 {
		t.Fatalf("dsInt64(uint8(1)) = %d", got)
	}
	if got := dsUint64(int64(42)); got != 42 {
		t.Fatalf("dsUint64(42) = %d", got)
	}
	if got := dsTime("2026-11-25 02:30:00.123"); got.IsZero() || got.Year() != 2026 {
		t.Fatalf("dsTime = %v", got)
	}
}
