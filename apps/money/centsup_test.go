package money_test

import (
	"testing"

	"github.com/hanzoai/cloud/apps/money"
)

// CentsUp is what a cents-only GATE reads. Rounding to nearest is what lets a
// sub-half-cent charge arrive as zero, and a zero charge is one a spend cap does
// not weigh at all — so the direction has to be away from zero, and it has to be
// exact at the boundaries.
func TestCentsUpNeverUnderstates(t *testing.T) {
	for _, c := range []struct {
		in        string
		cents, up int64
	}{
		{"0", 0, 0},
		{"0.000000000000000001", 0, 1}, // one atto: Cents says nothing is owed
		{"0.001", 0, 1},                // a tenth of a cent
		{"0.004", 0, 1},
		{"0.005", 1, 1},
		{"0.01", 1, 1}, // exact cent: no inflation
		{"0.011", 1, 2},
		{"1", 100, 100},
		{"1.005", 101, 101},
		{"-0.001", 0, -1}, // magnitude away from zero, sign preserved
		{"-0.01", -1, -1},
	} {
		a, err := money.ParseUSD(c.in)
		if err != nil {
			t.Fatalf("parse %s: %v", c.in, err)
		}
		if got := a.Cents(); got != c.cents {
			t.Errorf("%s: Cents() = %d, want %d", c.in, got, c.cents)
		}
		if got := a.CentsUp(); got != c.up {
			t.Errorf("%s: CentsUp() = %d, want %d", c.in, got, c.up)
		}
	}
}
