package coding

import (
	"testing"
)

// TestNormalizeApostrophes tests that typographic apostrophes and quotes are normalized.
func TestNormalizeApostrophes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"I\u2019ll do it", "I'll do it"},   // right single quotation mark
		{"I\u2019m working", "I'm working"}, // right single quotation mark
		{"\u2018Hello\u2019", "'Hello'"},    // left/right single quotes
		{"\u201cHello\u201d", "\"Hello\""},  // left/right double quotes
		{"test\u02bcstring", "test'string"}, // modifier letter apostrophe
		{"test\uff07string", "test'string"}, // fullwidth apostrophe
		{"I\u2019ll read the file, then I\u2019ll patch it", "I'll read the file, then I'll patch it"},
		{"Now I\u2019m patching the tests", "Now I'm patching the tests"},
	}
	for _, tc := range cases {
		got := normalizeApostrophes(tc.in)
		if got != tc.want {
			t.Errorf("normalizeApostrophes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNarrating_ProductionFailures tests the 4 exact production strings that were
// incorrectly not detected as narrating (causing the settle loop to break).
func TestNarrating_ProductionFailures(t *testing.T) {
	// These are the exact strings from the production failures (hanzoai/ai DEV-3 and hanzo-inc/kai)
	// Note: the apostrophes in the source are U+2019 (’)
	productionCases := []string{
		"The existing test scaffold is already present. I\u2019m reading its contracts and the tier lookup before patching them together.",
		"I found the shared mapping and native retry helper; now I\u2019m patching those and the required tests.",
		"The named files are clean. I\u2019ll read them and check for local instructions before patching.",
		"I\u2019m reading the three named files now, then I\u2019ll make and test the canonicalization fix.",
	}

	for _, tc := range productionCases {
		if !narrating(tc) {
			t.Errorf("narrating(%q) = false, want true (production failure case)", tc)
		}
	}
}

// TestNarrating_PresentProgressiveAndSequencing tests the new present-progressive
// and sequencing announcement patterns added to catch more narrating cases.
func TestNarrating_PresentProgressiveAndSequencing(t *testing.T) {
	cases := []string{
		// present-progressive
		"I'm patching the file now.",
		"Now I'm reading the config.",
		"I am now editing the tests.",
		"I'm going to run the tests.",
		"I'll run the build.",
		"I will check the output.",
		// sequencing
		"Next, I'll run the tests.",
		"Then I'll update the config.",
		", then I'll fix the bug.",
		", then I will run it.",
		"Before patching the file.",
		"Before editing the tests.",
		"Now I'm patching the changes.",
		"I'm patching the code.",
		"I'm reading the documentation.",
	}

	for _, tc := range cases {
		if !narrating(tc) {
			t.Errorf("narrating(%q) = false, want true", tc)
		}
	}
}

// TestNarrating_GenuineFinalAnswers tests that genuine final answers are NOT
// detected as narrating (they should pass through as settled or just not narrating).
func TestNarrating_GenuineFinalAnswers(t *testing.T) {
	cases := []string{
		// Final summaries with past tense - should NOT be narrating
		"I've updated the config and the tests pass.",
		"I've fixed the bug and verified the build.",
		"Updated the file and all tests pass.",
		"Done. The changes are complete and tests pass.",
		"The fix is applied and everything works.",
		"Changes committed and tests pass.",
		// Explicit "No change needed" - should be settled
		"No change needed: the code is already correct.",
		"No change needed: the test already covers this case.",
		"No changes are needed: the implementation is complete.",
		"Nothing to change: the feature is already implemented.",
		// Statements that are NOT announcements of future work
		"I'll leave it as is: No change needed",
		"The implementation is complete.",
		"All tests pass.",
	}

	for _, tc := range cases {
		if narrating(tc) {
			t.Errorf("narrating(%q) = true, want false (genuine final answer)", tc)
		}
	}
}

// TestSettled_WithTypographicApostrophes tests that settled() works with
// typographic apostrophes after normalization.
func TestSettled_WithTypographicApostrophes(t *testing.T) {
	cases := []string{
		"No change needed: the file is already correct.",
		"No changes are needed: everything is in place.",
		"Nothing to change: the tests already pass.",
		// With typographic apostrophes (U+2019)
		"No change needed: it\u2019s already done.",
		"No changes needed: nothing\u2019s broken.",
	}
	for _, tc := range cases {
		if !settled(tc) {
			t.Errorf("settled(%q) = false, want true", tc)
		}
	}
}

// TestSettled_NotNarrating tests that settled answers are not narrating.
func TestSettled_NotNarrating(t *testing.T) {
	cases := []string{
		"No change needed: the code is already correct.",
		"No changes are needed: the implementation is complete.",
		"Nothing to change: the feature is already implemented.",
	}
	for _, tc := range cases {
		if narrating(tc) {
			t.Errorf("narrating(%q) = true, want false (should be settled)", tc)
		}
	}
}
