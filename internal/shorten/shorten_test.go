package shorten_test

import (
	"testing"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/shorten"
)

// TestToNeverSplitsACharacter is the whole point: cutting on the byte offset alone
// leaves half a rune, and the half travels into a JSON field or a stored sample as
// invalid UTF-8. "é" is two bytes, "→" is three, an emoji is four, so a bound that
// lands inside any of them must step back to where the character starts.
func TestToNeverSplitsACharacter(t *testing.T) {
	for _, s := range []string{"héllo wörld", "a→b→c", "naïve café", "🙂🙃🙂", "aé🙂→z"} {
		for n := range len(s) + 2 {
			got := shorten.To(s, n)
			if !utf8.ValidString(got) {
				t.Errorf("To(%q, %d) = %q — not valid UTF-8", s, n, got)
			}
			if len(got) > n {
				t.Errorf("To(%q, %d) = %q — %d bytes, over the bound", s, n, got, len(got))
			}
		}
	}
}

// TestToKeepsEveryCharacterThatFits: stepping back must not be greedy. A bound that
// lands exactly at a character boundary keeps everything up to it.
func TestToKeepsEveryCharacterThatFits(t *testing.T) {
	const s = "aé🙂" // 1 + 2 + 4 bytes
	for n, want := range map[int]string{0: "", 1: "a", 2: "a", 3: "aé", 4: "aé", 7: "aé🙂", 9: "aé🙂"} {
		if got := shorten.To(s, n); got != want {
			t.Errorf("To(%q, %d) = %q, want %q", s, n, got, want)
		}
	}
}

// TestToLeavesAShortStringAlone, and refuses a nonsense bound rather than panicking.
func TestToLeavesAShortStringAlone(t *testing.T) {
	if got := shorten.To("abc", 10); got != "abc" {
		t.Errorf("To = %q, want abc", got)
	}
	if got := shorten.To("abc", -1); got != "" {
		t.Errorf("To with a negative bound = %q, want empty", got)
	}
	if got := shorten.To("", 5); got != "" {
		t.Errorf("To of empty = %q, want empty", got)
	}
}

// TestASCIIIsCutExactly — the common case must not have become approximate.
func TestASCIIIsCutExactly(t *testing.T) {
	if got := shorten.To("abcdefgh", 3); got != "abc" {
		t.Errorf("To = %q, want abc", got)
	}
}
