package plan

// Tests for the catalog-driven paid-tier predicate, run against the REAL embedded
// @hanzo/plans catalog (no fixture) — so they also pin the owner's "Pro ($20/mo) and
// above" cut to the catalog's actual contents.

import "testing"

func TestPaid_CoreTiersArePaid(t *testing.T) {
	// The owner's cut — Pro/Plus/Max/Team/Enterprise — plus their paid supersets
	// (Team Max) and the negotiated enterprise contract (Custom).
	for _, id := range []string{"pro", "plus", "max", "team", "team-max", "enterprise", "custom"} {
		paid, err := Paid(id)
		if err != nil {
			t.Fatalf("Paid(%q) error: %v", id, err)
		}
		if !paid {
			t.Errorf("Paid(%q) = false, want true (a paid cloud tier)", id)
		}
	}
}

func TestPaid_FreeAndNonAccountAreNotPaid(t *testing.T) {
	// developer = free cloud tier; world-*/social-* = separate product lines (not a
	// cloud account tier); unknown/empty = never paid.
	for _, id := range []string{"developer", "world-free", "world-pro", "social-pro", "social-free", "", "nonexistent-tier"} {
		paid, err := Paid(id)
		if err != nil {
			t.Fatalf("Paid(%q) error: %v", id, err)
		}
		if paid {
			t.Errorf("Paid(%q) = true, want false (free / non-account / unknown)", id)
		}
	}
}
