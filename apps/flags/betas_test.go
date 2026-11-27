// Copyright © 2026 Hanzo AI. MIT License.

package flags

import "testing"

// The two production beta sites are registered as ordinary principal switches,
// and staff coverage comes from the ONE beta.* rule in access.go (staffed) —
// not from a second list here. IAM reads these keys by the redirect host.
func TestBetas_GateSitesAreRegisteredAndStaffed(t *testing.T) {
	for _, tc := range []struct {
		key   string
		label string
	}{
		{BetaKey("bot"), "Bot"},
		{BetaKey("team"), "Team"},
	} {
		d, ok := lookupDef(tc.key)
		if !ok {
			t.Fatalf("%s is not registered; add it to Betas", tc.key)
		}
		if !d.Principal || d.Category != "Beta" || d.Label != tc.label || d.Type != TypeBool || d.Default != "false" {
			t.Fatalf("%s definition = %+v, want a Beta principal bool default false labelled %q", tc.key, d, tc.label)
		}
		if !staffed(tc.key) {
			t.Fatalf("staffed(%q) = false: a SuperAdmin would not pass the site gate", tc.key)
		}
	}
}
