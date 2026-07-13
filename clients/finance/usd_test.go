package finance

import "testing"

// TestUsdToCents locks the ai-UsageEvent.USD -> ledger-cents boundary: round-half-up,
// sub-cent floors to 0 (the cents ledger's contract), malformed -> 0. Money-safety.
func TestUsdToCents(t *testing.T) {
	cases := []struct {
		usd  string
		want int64
	}{
		{"0.00132", 0},   // sub-cent -> 0 (skipped; cents ledger, not atto)
		{"0.005", 1},     // round-half-up
		{"0.004", 0},     // round down
		{"1.005", 101},   // half rounds up
		{"1.50", 150},
		{"12.34", 1234},
		{"1000000", 100000000},
		{"0", 0},
		{"", 0},          // empty -> 0
		{"bad", 0},       // malformed -> 0, never a spurious debit
		{"-1.00", -100},  // sign preserved (a credit/adjustment)
	}
	for _, tc := range cases {
		if got := usdToCents(tc.usd); got != tc.want {
			t.Errorf("usdToCents(%q) = %d, want %d", tc.usd, got, tc.want)
		}
	}
}
