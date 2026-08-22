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
