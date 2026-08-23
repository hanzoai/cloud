// Copyright © 2026 Hanzo AI. MIT License.

package tel

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/shorten"
)

// numberRow.ours is where the carrier's vocabulary becomes ours, and where a
// MONTHLY RATE stops being a decimal string and becomes minor units. It is the
// one arithmetic in this package that reaches an invoice, and it had no test.

// TestARateBecomesMinorUnitsExactly is the claim the comment above ours makes: a
// rate that is exact on the invoice must not become approximate on the way to it.
// The cases below are the ones binary floating point gets wrong if the rounding
// is dropped — 1.15 and 1.005 are not representable, and truncating instead of
// rounding bills a cent short on each.
func TestARateBecomesMinorUnitsExactly(t *testing.T) {
	for _, c := range []struct {
		cost string
		want int64
	}{
		{"0", 0},
		{"1", 100},
		{"1.15", 115},
		{"0.50", 50},
		{"0.05", 5},
		{"2.99", 299},
		{"10.00", 1000},
		{"12.34", 1234},
		{"99.99", 9999},
		{"1000", 100000},
	} {
		got := numberRow{Cost: c.cost}.ours().Monthly
		if got != c.want {
			t.Errorf("cost %q = %d minor units, want %d", c.cost, got, c.want)
		}
	}
}

// TestACostThisCannotReadIsFree is the CURRENT behaviour, written down because it
// is the risk in this function rather than a property to be pleased about: a cost
// the parser refuses becomes zero, and a zero monthly rate is a free number.
//
// Nothing here is wrong today — the carrier sends a bare decimal. But a carrier
// that starts sending "$1.15", or a locale that sends "1,15", would make every
// number on the invoice free, and the failure is silent because a parse error is
// discarded. A test that says so is what makes the next reader decide it on
// purpose.
func TestACostThisCannotReadIsFree(t *testing.T) {
	for _, unreadable := range []string{"", "  ", "$1.15", "1,15", "free", "1.15 USD"} {
		if got := (numberRow{Cost: unreadable}.ours().Monthly); got != 0 {
			t.Errorf("cost %q = %d, want 0 — this documents the fallback, not an endorsement", unreadable, got)
		}
	}
}

// TestTheCarriersFieldsBecomeOurs pins the rest of the translation. Every other
// file in this package speaks Number, so a field dropped here is a field nothing
// downstream can recover.
func TestTheCarriersFieldsBecomeOurs(t *testing.T) {
	got := numberRow{
		ID:          "num_1",
		PhoneNumber: "+15555550123",
		Country:     "US",
		Type:        "local",
		Features:    []string{"sms", "voice"},
		Cost:        "1.15",
		Currency:    "usd",
	}.ours()

	if got.ID != "num_1" || got.E164 != "+15555550123" {
		t.Errorf("identity = %q / %q, want the carrier's id and number", got.ID, got.E164)
	}
	if got.Country != "US" || got.Type != "local" || got.Currency != "usd" {
		t.Errorf("country/type/currency = %q / %q / %q", got.Country, got.Type, got.Currency)
	}
	if len(got.Capable) != 2 || got.Capable[0] != "sms" || got.Capable[1] != "voice" {
		t.Errorf("capabilities = %v, want both the carrier listed", got.Capable)
	}
	if got.Monthly != 115 {
		t.Errorf("monthly = %d, want 115", got.Monthly)
	}
}

// TestACarriersMessageIsTrimmedWithoutBreakingARune guards the failure path this
// package reports through. It truncated by BYTES, which splits a multi-byte rune
// and puts invalid UTF-8 into the error a human reads; internal/shorten is the
// one place that is done correctly, and this is now that one place.
func TestACarriersMessageIsTrimmedWithoutBreakingARune(t *testing.T) {
	// A message whose 300-byte boundary lands mid-rune.
	msg := strings.Repeat("a", 299) + "é" + strings.Repeat("b", 50)

	got := shorten.To(msg, 300)

	if len(got) > 300 {
		t.Errorf("trimmed to %d bytes, want at most 300", len(got))
	}
	for i, r := range got {
		if r == '�' {
			t.Errorf("byte %d of the trimmed message is not valid UTF-8: %q", i, got)
			break
		}
	}
}
