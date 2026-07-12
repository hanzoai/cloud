package finance

import "testing"

// TestFundingClass locks the provider-level funding classification the
// /v1/admin/usage/funding endpoint derives from the credit ledger.
func TestFundingClass(t *testing.T) {
	cases := []struct {
		name string
		pc   ProviderCredit
		want string
	}{
		{"grant with remaining -> credit", ProviderCredit{HasCredit: true, RemainingCents: 2_500_000}, "credit"},
		{"grant exhausted -> paid", ProviderCredit{HasCredit: true, RemainingCents: 0}, "paid"},
		{"no grant -> paid_only", ProviderCredit{HasCredit: false, RemainingCents: 0}, "paid_only"},
		{"no grant, stray remaining -> paid_only", ProviderCredit{HasCredit: false, RemainingCents: 100}, "paid_only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fundingClass(tc.pc); got != tc.want {
				t.Errorf("fundingClass(%+v) = %q, want %q", tc.pc, got, tc.want)
			}
		})
	}
}

// TestAI64 covers the ClickHouse-cell coercion across the driver/JSON transports
// (sum()/count() come back as uint64, float64, or decimal-string depending on path).
func TestAI64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(42), 42},
		{int(42), 42},
		{uint64(42), 42},
		{float64(42), 42},
		{"42", 42},
		{"2600000", 2_600_000},
		{nil, 0},
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		if got := aI64(tc.in); got != tc.want {
			t.Errorf("aI64(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestProviderGrantsSeeded asserts DO's $26k grant is the seeded real row (the
// live-verifiable acceptance) and is treated as credit-bearing.
func TestProviderGrantsSeeded(t *testing.T) {
	if providerGrantsCents["do-ai"] != 2_600_000 {
		t.Fatalf("do-ai grant = %d cents, want 2_600_000 ($26k)", providerGrantsCents["do-ai"])
	}
	if fundingClass(ProviderCredit{HasCredit: true, RemainingCents: providerGrantsCents["do-ai"]}) != "credit" {
		t.Error("seeded DO grant with full remaining must classify as credit")
	}
}
