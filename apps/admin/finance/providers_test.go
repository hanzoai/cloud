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

// TestAI64 covers the datastore-cell coercion across the driver/JSON transports
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

// TestDOGrantIsDiscoveredNotSeeded pins the inversion: DO's grant must NOT be a
// constant in this map. It is read from DO's own invoices (Client.CreditIssued),
// because the hand-entered value was wrong and unverifiable — this map said
// $26,000 while DO's ledger showed $21,263.65 ever applied and the operator
// believed $50,000. A seeded number here would silently win again.
func TestDOGrantIsDiscoveredNotSeeded(t *testing.T) {
	if v, ok := providerGrantsCents["do-ai"]; ok {
		t.Fatalf("do-ai must not carry a seeded grant (got %d cents); it is discovered from DO invoices", v)
	}
}

// TestExhaustedCreditClassifiesAsPaid is the whole point of the funding class: a
// provider whose promo credit is SPENT is cash from that moment on, even though a
// grant certainly existed. Classifying on "was a grant ever issued" instead of
// "is credit left" is what let $1,824 of real DO spend read as credit-funded.
func TestExhaustedCreditClassifiesAsPaid(t *testing.T) {
	live := ProviderCredit{GrantCents: 2_126_712, RemainingCents: 347, HasCredit: true}
	if got := fundingClass(live); got != "credit" {
		t.Errorf("credit remaining => credit-funded, got %q", got)
	}
	spent := ProviderCredit{GrantCents: 2_126_712, RemainingCents: 0, HasCredit: false, IsPaidOnly: true}
	if got := fundingClass(spent); got == "credit" {
		t.Error("an EXHAUSTED grant must never classify as credit — every later call is cash")
	}
}
