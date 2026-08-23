// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"testing"
	"time"
)

// THE FLOOR IS THE POINT. Every caller's floor is the constant it charged before
// the authority existed, so the property that has to hold in every failure is
// that metered work keeps costing what it cost yesterday — never zero, and never
// an error that stops the work over a price.

// With no commerce beside this process there is no plane to ask, so the ask
// fails and the floor is charged. This is the ordinary case for a split deploy:
// storage, translate and risk each run in their own binary.
func TestRateNano_ChargesTheFloorWithNoAuthorityBeside(t *testing.T) {
	for _, floor := range []int64{80_000_000, 20_000, 100_000, 1} {
		if got := RateNano(context.Background(), "storage", "block-gb-month", floor); got != floor {
			t.Errorf("floor %d resolved to %d — a price that cannot be read must not change "+
				"what metered work costs", floor, got)
		}
	}
}

// A FLOOR OF ZERO IS HONOURED, because zero is a real price: something the
// platform meters and gives away. The helper must not invent a minimum.
func TestRateNano_DoesNotInventAMinimum(t *testing.T) {
	if got := RateNano(context.Background(), "translate", "bulk-chars", 0); got != 0 {
		t.Errorf("a floor of zero resolved to %d; the helper substituted a price nobody chose", got)
	}
}

// An already-cancelled context must not hang and must not fail the work: it
// resolves to the floor like any other unreadable price.
func TestRateNano_ACancelledCallStillPrices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := RateNano(ctx, "risk", "screen", 100_000); got != 100_000 {
		t.Errorf("a cancelled call resolved to %d, want the floor 100000", got)
	}
}

// The ask is BOUNDED. A price is not worth waiting on — the floor is a real
// number that was charging yesterday — so an unwell authority must not hold a
// customer's request open.
func TestRateNano_IsBounded(t *testing.T) {
	if rateCallTimeout <= 0 {
		t.Fatal("the rate ask is unbounded; an unwell authority would hold every metered " +
			"request open for as long as it stayed unwell")
	}
	if rateCallTimeout > 5*time.Second {
		t.Errorf("the rate ask waits up to %s on a number that has a known floor", rateCallTimeout)
	}

	start := time.Now()
	RateNano(context.Background(), "storage", "block-gb-month", 1)
	if el := time.Since(start); el > rateCallTimeout+2*time.Second {
		t.Errorf("the ask took %s, past its own %s bound", el, rateCallTimeout)
	}
}

// A meter named with neither part is still priced at the floor rather than
// panicking or charging zero: the caller passed a constant, and that constant is
// the answer whatever the lookup makes of the name.
func TestRateNano_AHalfNamedMeterStillTakesTheFloor(t *testing.T) {
	for _, tc := range []struct{ product, meter string }{
		{"", ""},
		{"storage", ""},
		{"", "screen"},
	} {
		if got := RateNano(context.Background(), tc.product, tc.meter, 42); got != 42 {
			t.Errorf("RateNano(%q,%q) = %d, want the floor 42", tc.product, tc.meter, got)
		}
	}
}

// THE UNIT WRAPPERS CONVERT BOTH WAYS — the floor up, the answer down — and a
// round trip cannot see a wrong factor: multiply and divide by the same wrong
// number and the floor comes back unchanged. So this pins the floor in the
// CALLER's unit, which is the whole point of the wrappers, and the test below
// pins the factors themselves.
func TestRateUnitsRoundTripTheFloor(t *testing.T) {
	ctx := context.Background()
	for _, floor := range []int64{0, 1, 8, 20, 100, 999_999} {
		if got := RateCents(ctx, "storage", "block-gb-month", floor); got != floor {
			t.Errorf("RateCents floor %d came back as %d", floor, got)
		}
		if got := RateMicros(ctx, "risk", "screen", floor); got != floor {
			t.Errorf("RateMicros floor %d came back as %d", floor, got)
		}
	}
}

// The factors are the real ones, read in ONE direction against arithmetic anyone
// can check — which is the half the round trip above is blind to.
func TestTheUnitFactorsAreWhatTheyClaim(t *testing.T) {
	if nanoPerCent != 10_000_000 {
		t.Errorf("nanoPerCent = %d; a cent is 10^-2 USD and a nano-dollar 10^-9, so it is 10^7", nanoPerCent)
	}
	if nanoPerMicro != 1_000 {
		t.Errorf("nanoPerMicro = %d; a micro-USD is 10^-6 USD, so it is 10^3", nanoPerMicro)
	}
	// Tied to a number that is actually charged: $0.08/GB-month is 8 cents is
	// 80,000,000 nano — the storage meter's seeded value.
	if got := int64(8) * nanoPerCent; got != 80_000_000 {
		t.Errorf("8 cents = %d nano, want 80000000 — the seeded storage rate", got)
	}
	if got := int64(20) * nanoPerMicro; got != 20_000 {
		t.Errorf("20 uUSD = %d nano, want 20000 — the seeded translate rate", got)
	}
}
