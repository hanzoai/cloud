package datastore

import (
	"fmt"
	"testing"
)

// spend evaluates the SHIPPED Spend expression the way the warehouse does: sum the
// rows' cost_nano, then apply the one intDiv. It reads the constant rather than
// restating its arithmetic, so a Spend that went back to rounding per row — or that
// changed the divisor — cannot pass while this test's own numbers sit untouched.
func spend(t *testing.T, nano []int64) int64 {
	t.Helper()
	var add, div int64
	if _, err := fmt.Sscanf(Spend, "intDiv(sum(cost_nano) + %d, %d)", &add, &div); err != nil {
		t.Fatalf("Spend no longer sums the money and rounds once: %q (%v)", Spend, err)
	}
	var summed int64
	for _, n := range nano {
		summed += n // sum(cost_nano)
	}
	return (summed + add) / div // intDiv(... + add, div)
}

// rows is n calls that each cost the same.
func rows(n int, nano int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = nano
	}
	return out
}

// TestSpend is the arithmetic every spend board depends on. A served call routinely
// costs a fraction of a cent, so the cases that matter are the ones with many small
// rows: they are exactly where rendering each row in cents and adding the renderings
// stops being a rounding error and becomes a number that has nothing to do with the
// money.
func TestSpend(t *testing.T) {
	const (
		tenth = 1_000_000  // $0.001
		cent  = 10_000_000 // $0.01
	)
	cases := []struct {
		name string
		nano []int64
		want int64
	}{
		{"a hundred calls at four tenths of a cent cost forty cents", rows(100, 4*tenth), 40},
		{"a thousand calls at a tenth of a cent cost a dollar", rows(1000, tenth), 100},
		{"calls that cost nothing cost nothing", rows(3911, 0), 0},
		{"half a cent rounds up", []int64{cent / 2}, 1},
		{"a hair under half a cent rounds down", []int64{cent/2 - 1}, 0},
		{"one call is worth itself", []int64{123_456_789}, 12},
		{"a mixed window is the sum, then the rounding", []int64{4 * tenth, 6 * tenth, tenth / 2}, 1},
		{"an empty window is zero, not an error", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spend(t, c.nano); got != c.want {
				t.Fatalf("spend = %d cents, want %d", got, c.want)
			}
		})
	}

	// The headline case, stated as the contrast: a cent per sub-cent call would
	// report $1.00 for $0.40 of traffic, and the error grows with the call count.
	if got, perCall := spend(t, rows(100, 4*tenth)), int64(100); got == perCall {
		t.Fatalf("spend charged a cent per call: %d", got)
	}
}
