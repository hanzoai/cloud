package money

import "testing"

// TestCentsReadAsWritten pins the rendering at the places three separate copies of
// it used to disagree: the zero, the sub-dime remainder that needs its leading
// zero, the whole dollar that has to keep its ".00", and the sign.
func TestCentsReadAsWritten(t *testing.T) {
	for _, tc := range []struct {
		in   Cents
		want string
	}{
		{0, "$0.00"},
		{1, "$0.01"},
		{7, "$0.07"},
		{10, "$0.10"},
		{99, "$0.99"},
		{100, "$1.00"},
		{99900, "$999.00"},
		{999999, "$9999.99"},
		{-1, "-$0.01"},
		{-99900, "-$999.00"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Cents(%d) = %q, want %q", int64(tc.in), got, tc.want)
		}
	}
}

// TestCentsRoundTripThroughAmount is the join between this package's two halves: a
// figure that becomes an Amount to be multiplied comes back as the same Cents, so
// nothing is lost by crossing between them.
func TestCentsRoundTripThroughAmount(t *testing.T) {
	for _, c := range []Cents{0, 1, 7, 100, 2344, 999999, -4_000_000} {
		if got := Cents(FromCents(int64(c)).Cents()); got != c {
			t.Errorf("Cents(%d) → Amount → %d", int64(c), int64(got))
		}
	}
}
