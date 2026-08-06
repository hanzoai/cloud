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

// THE THREE MINOR VARIANTS ARE A CHOICE, AND THE CHOICE IS THE POINT.
//
// An adversarial review found two call sites that had picked the wrong one, both
// silently:
//
//   - apps/admin/finance/backfill.go used Minor() on a BALANCE BEING MIGRATED.
//     Minor() refuses a sub-cent amount, and every org that has spent anything
//     carries a sub-cent tail, so the finance backfill migrated ZERO for every
//     active org while returning an honest-looking error.
//   - apps/admin/moneyboard.go used Minor() on a DISPLAY. The treasury reserve has
//     the same tail, so the board rendered "could not reach the treasury" for a
//     treasury that was reachable.
//
// The rule, and why each direction:
//
//	Minor()       a DEBIT      — exact or refuse. Rounding money you TAKE is theft
//	                             in one direction and a gift in the other.
//	FloorMinor()  a COMPARISON — never overstate. Admitting spend a balance cannot
//	              or MIGRATION   cover leaves a negative balance nobody authorised;
//	                             migrating more than is held mints the difference.
//	RoundMinor()  a DISPLAY    — nearest. Nothing is spent from it, and refusing to
//	                             show a figure is worse than a half-cent.
func TestTheThreeMinorVariantsDifferWhereItMatters(t *testing.T) {
	// The real production balance, from apps/billing/balance.go's own comment.
	const prod = "149913.078983985999994361"
	m := Money{Decimal: prod, Currency: "USD"}

	if _, err := m.Minor(); err == nil {
		t.Error("Minor() accepted a sub-cent amount — a debit must refuse rather than " +
			"round money it is about to take")
	}
	floor, err := m.FloorMinor()
	if err != nil {
		t.Fatalf("FloorMinor: %v", err)
	}
	round, err := m.RoundMinor()
	if err != nil {
		t.Fatalf("RoundMinor: %v", err)
	}
	if floor != 14991307 {
		t.Errorf("FloorMinor = %d, want 14991307", floor)
	}
	if round != 14991308 {
		t.Errorf("RoundMinor = %d, want 14991308", round)
	}
	if round <= floor {
		t.Error("this value must distinguish the two variants, or the test proves nothing")
	}
}

// RoundMinor rounds to NEAREST and is total where Minor refuses — but it is still
// money arithmetic, so it must not wrap or invent a value.
func TestRoundMinorIsNearestAndStillRefusesTheImpossible(t *testing.T) {
	for _, c := range []struct {
		decimal string
		want    int64
	}{
		{"4.994", 499},
		{"4.995", 500}, // half away from zero
		{"5.00", 500},
		{"-4.995", -500},
		{"0.004", 0},
	} {
		got, err := Money{Decimal: c.decimal, Currency: "USD"}.RoundMinor()
		if err != nil {
			t.Errorf("RoundMinor(%s): %v", c.decimal, err)
			continue
		}
		if got != c.want {
			t.Errorf("RoundMinor(%s) = %d, want %d", c.decimal, got, c.want)
		}
	}
	if _, err := (Money{Decimal: "99999999999999999999.99", Currency: "USD"}).RoundMinor(); err == nil {
		t.Error("an amount beyond int64 minor units must error, not wrap")
	}
	if _, err := (Money{Decimal: "not-a-number", Currency: "USD"}).RoundMinor(); err == nil {
		t.Error("a malformed amount must error, never round to zero")
	}
}
