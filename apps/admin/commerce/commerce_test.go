package commerce

import "testing"

// TestMonthlyNormalizedCents proves annual/monthly normalization for MRR.
func TestMonthlyNormalizedCents(t *testing.T) {
	if got := monthlyNormalized(12_000, "year"); got != 1_000 {
		t.Errorf("yearly $120 → monthly = %d, want 1000", got)
	}
	if got := monthlyNormalized(2_000, "month"); got != 2_000 {
		t.Errorf("monthly must pass through, got %d", got)
	}
	if got := monthlyNormalized(2_000, ""); got != 2_000 {
		t.Errorf("unknown interval must be treated as monthly, got %d", got)
	}
}
