package plane

import "testing"

// RoundMinor is the explicit display rounding Minor() demands of summaries.
// The first row is the production 502: a real per-token debit finer than a
// cent, which Minor() refuses and a usage page must still show.
func TestRoundMinor(t *testing.T) {
	cases := []struct {
		dec  string
		want int64
	}{
		{"0.00589", 1},    // the 502: sub-cent rounds half-away to one cent
		{"0.004", 0},      // below half stays zero
		{"0.005", 1},      // exact half rounds away from zero
		{"-0.005", -1},    // ...in both directions
		{"12.34", 1234},   // exact amounts pass through unchanged
		{"4.995", 500},    // the FloorMinor doc's balance case — HERE it rounds up,
		// which is why RoundMinor must never gate a spend (FloorMinor's job)
	}
	for _, c := range cases {
		m := Money{Decimal: c.dec, Currency: "USD"}
		got, err := m.RoundMinor()
		if err != nil {
			t.Fatalf("RoundMinor(%s): %v", c.dec, err)
		}
		if got != c.want {
			t.Errorf("RoundMinor(%s) = %d, want %d", c.dec, got, c.want)
		}
	}
	// Minor() still refuses the sub-cent row — the guard this method
	// deliberately drops must keep existing where debits are taken.
	if _, err := (Money{Decimal: "0.00589", Currency: "USD"}).Minor(); err == nil {
		t.Error("Minor(0.00589) accepted a sub-cent amount — the exactness guard is gone")
	}
}
