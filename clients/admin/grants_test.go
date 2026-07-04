package admin

import "testing"

// TestGrantTag pins the money-bucket mapping: a staff comp defaults to the
// non-cash TRIAL (Credit) bucket, and ONLY an explicit "prepaid" mints real
// money. A tagging slip must never silently create payout-able cash.
func TestGrantTag(t *testing.T) {
	cases := []struct {
		source   string
		wantTag  string
		wantNorm string
	}{
		{"", "grant:admin", "trial"},          // default → trial (fail-closed non-cash)
		{"trial", "grant:admin", "trial"},     // explicit trial
		{"TRIAL", "grant:admin", "trial"},     // case-insensitive
		{"  trial ", "grant:admin", "trial"},  // trimmed
		{"comp", "grant:admin", "trial"},      // unknown → trial (never real money by accident)
		{"prepaid", "admin-grant", "prepaid"}, // explicit real money
		{"PREPAID", "admin-grant", "prepaid"}, // case-insensitive
		{" prepaid", "admin-grant", "prepaid"},
	}
	for _, tc := range cases {
		tag, norm := grantTag(tc.source)
		if tag != tc.wantTag || norm != tc.wantNorm {
			t.Errorf("grantTag(%q) = (%q,%q), want (%q,%q)", tc.source, tag, norm, tc.wantTag, tc.wantNorm)
		}
	}
}
