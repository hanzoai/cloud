// Copyright © 2026 Hanzo AI. MIT License.

package plane

import "testing"

// FloorMinor rounds DOWN, never up, at any magnitude or sign.
//
// The property is not decorative. money.Amount.Minor() rescales, and
// hanzoai/decimal's Rescale rounds HALF-AWAY-FROM-ZERO — so a 4.995 balance read
// as 500 cents and passed hanzoai/ai's `avail < priceCents` against a 500-cent
// charge it could not cover. Both call sites carried a comment claiming the call
// "truncates toward zero"; neither was ever true, and the test that guarded one of
// them only grepped the source for that sentence, so it confirmed the wrong claim
// was still written down. Assert the arithmetic instead.
func TestFloorMinorRoundsDownNeverUp(t *testing.T) {
	for _, c := range []struct {
		decimal string
		want    int64
		why     string
	}{
		{"4.995", 499, "the half-cent Rescale rounded UP to 500, past a 500-cent charge"},
		{"4.999999999999999999", 499, "eighteen decimals, still under five dollars"},
		{"5.00", 500, "exact amounts are untouched"},
		{"149918.078983985999994361", 14991807, "the real prod balance; Minor() gave …08"},
		{"0.009", 0, "a sub-cent balance is not a cent"},
		{"0", 0, "zero stays zero"},
		{"-0.005", -1, "a negative balance floors AWAY from zero; truncation would overstate it"},
		{"-4.995", -500, "an overdraft is never rounded back toward solvency"},
	} {
		got, err := Money{Decimal: c.decimal, Currency: "USD"}.FloorMinor()
		if err != nil {
			t.Errorf("FloorMinor(%s): %v", c.decimal, err)
			continue
		}
		if got != c.want {
			t.Errorf("FloorMinor(%s) = %d, want %d — %s", c.decimal, got, c.want, c.why)
		}
	}
}

// The scale is the CURRENCY's, exactly as Minor() takes it. Hardcoding 2 would
// divide a zero-decimal currency by a hundred and report a balance a hundredth of
// its true size — for JPY, effectively the whole balance.
func TestFloorMinorTakesScaleFromCurrency(t *testing.T) {
	got, err := Money{Decimal: "1234.9", Currency: "JPY"}.FloorMinor()
	if err != nil {
		t.Fatalf("JPY: %v", err)
	}
	if got != 1234 {
		t.Errorf("FloorMinor(1234.9 JPY) = %d, want 1234 (yen have no minor unit)", got)
	}
}

// Rounding down is not permission to widen: an amount too large for an int64 is an
// error, never a wrap. A wrapped balance is how a funded account reads as overdrawn.
func TestFloorMinorRefusesOverflowRatherThanWrapping(t *testing.T) {
	if _, err := (Money{Decimal: "99999999999999999999.99", Currency: "USD"}).FloorMinor(); err == nil {
		t.Error("an amount beyond int64 minor units must error, not wrap")
	}
}

// A malformed amount is an ERROR, never a zero — the same rule Parse states. Read as
// "nothing to spend", it would refuse a funded caller; read as "nothing to charge", it
// would let work through free.
func TestFloorMinorRefusesMalformed(t *testing.T) {
	if _, err := (Money{Decimal: "not-a-number", Currency: "USD"}).FloorMinor(); err == nil {
		t.Error("a malformed amount must error, never floor to zero")
	}
}
