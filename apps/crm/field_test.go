package crm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clip bounds every free-text field this package stores, and the fields hold what
// people type — names, notes, company names — so they carry accented letters,
// arrows and emoji. A bound of 1024 BYTES lands wherever it lands, and cutting on
// that offset alone used to split whatever character straddled it, storing a
// dangling half that is not valid UTF-8 and cannot be encoded back out as JSON.
//
// The bound itself is unchanged: still at most maxField bytes.
func TestAClippedFieldIsAlwaysValidUTF8(t *testing.T) {
	// Every multi-byte width, repeated past the bound, so the cut lands inside a
	// character for at least one of them whatever maxField happens to be.
	for _, ch := range []string{"é", "→", "🙂"} {
		s := strings.Repeat(ch, maxField)
		got := clip(s)
		if !utf8.ValidString(got) {
			t.Errorf("clip of %q x%d is not valid UTF-8 (%d bytes)", ch, maxField, len(got))
		}
		if len(got) > maxField {
			t.Errorf("clip of %q x%d = %d bytes, over the %d bound", ch, maxField, len(got), maxField)
		}
		// Nothing was lost beyond the straddling character: the result must reach
		// within one character's width of the bound.
		if maxField-len(got) >= len(ch)+1 {
			t.Errorf("clip of %q x%d gave up %d bytes of a %d bound", ch, maxField, maxField-len(got), maxField)
		}
	}
}

// TestClipStillTrimsAndPassesShortTextThrough — the surrounding behaviour is the
// same as it was; only the cut moved.
func TestClipStillTrimsAndPassesShortTextThrough(t *testing.T) {
	if got := clip("  Acme Corp \n"); got != "Acme Corp" {
		t.Errorf("clip = %q, want %q", got, "Acme Corp")
	}
	if got := clip("naïve café →"); got != "naïve café →" {
		t.Errorf("clip altered text that fits: %q", got)
	}
}
