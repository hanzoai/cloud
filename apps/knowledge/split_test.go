package knowledge

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkSpans holds split to its contract on one text: passages in order, the
// first at 0 and the last at the end; each one's own part (from where the
// previous ended) at most chunkSize; the reach back at most chunkOverlap and
// opening on a word; every boundary on a rune.
func checkSpans(t *testing.T, text string) []span {
	t.Helper()
	sp := split(text)
	if len(sp) == 0 {
		t.Fatalf("no passages for %d bytes of text", len(text))
	}
	if sp[0].lo != 0 || sp[len(sp)-1].hi != len(text) {
		t.Fatalf("passages do not cover the text: first %v, last %v, len %d", sp[0], sp[len(sp)-1], len(text))
	}
	for i, s := range sp {
		if s.lo >= s.hi || !utf8.RuneStart(text[s.lo]) || s.hi < len(text) && !utf8.RuneStart(text[s.hi]) {
			t.Fatalf("passage %d %v is empty or splits a rune", i, s)
		}
		if i == 0 {
			if s.hi > chunkSize {
				t.Fatalf("passage 0 is %d bytes", s.hi)
			}
			continue
		}
		prev := sp[i-1]
		if own := s.hi - prev.hi; own <= 0 || own > chunkSize {
			t.Fatalf("passage %d adds %d bytes", i, own)
		}
		if back := prev.hi - s.lo; back < 0 || back > chunkOverlap {
			t.Fatalf("passage %d reaches back %d bytes", i, back)
		}
		if s.lo < prev.hi && (s.lo == 0 || text[s.lo-1] != ' ' && text[s.lo-1] != '\n') {
			t.Fatalf("passage %d opens mid-word: %q", i, text[s.lo:min(s.lo+20, s.hi)])
		}
	}
	return sp
}

// TestSplitTilesLongProse: a 25 KB page becomes about a dozen passages that
// cover it, and its last sentence is in the last passage.
func TestSplitTilesLongProse(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < 25000; i++ {
		fmt.Fprintf(&b, "Sentence %d is about routine work on the staging cluster. ", i)
		if i%7 == 6 {
			b.WriteString("\n\n")
		}
	}
	b.WriteString("The last sentence names the tail.")
	text := b.String()
	sp := checkSpans(t, text)
	if n := len(sp); n < 12 || n > 20 {
		t.Fatalf("%d passages for %d bytes", n, len(text))
	}
	if last := text[sp[len(sp)-1].lo:]; !strings.Contains(last, "names the tail") {
		t.Fatalf("the tail is not in the last passage: %q", last)
	}
}

// TestSplitCutsBeforeHeadings: two sections that do not fit one passage are cut
// where the second heading starts, not mid-section.
func TestSplitCutsBeforeHeadings(t *testing.T) {
	body := strings.Repeat("word ", 300) // 1500 bytes
	text := "# First\n\n" + body + "\n\n## Second\n\n" + body
	sp := checkSpans(t, text)
	if len(sp) != 2 {
		t.Fatalf("%d passages, want 2", len(sp))
	}
	if at := strings.Index(text, "## Second"); sp[0].hi != at {
		t.Fatalf("first passage ends at %d, the heading is at %d", sp[0].hi, at)
	}
}

// TestSplitKeepsFencesWhole: a fenced block's blank lines are not paragraph
// breaks, so no passage boundary falls inside the fence.
func TestSplitKeepsFencesWhole(t *testing.T) {
	code := "```go\n" + strings.Repeat("x := 1\n\ny := 2\n\n", 60) + "```\n"
	text := strings.Repeat("lead ", 300) + "\n\n" + code + "\nafter the fence\n"
	lo := strings.Index(text, "```go")
	hi := strings.LastIndex(text, "```") + 3
	for _, s := range checkSpans(t, text) {
		if s.hi > lo && s.hi < hi {
			t.Fatalf("a passage ends inside the fence at %d (fence %d..%d)", s.hi, lo, hi)
		}
	}
}

// TestSplitNeverSplitsARune: text with no space or newline to cut at is cut at
// the bound, stepped back to a rune boundary — in every script.
func TestSplitNeverSplitsARune(t *testing.T) {
	for _, unit := range []string{"é", "日本語", "🙂", "a"} {
		text := strings.Repeat(unit, 5000/len(unit)+1)
		for _, s := range checkSpans(t, text) {
			if !utf8.ValidString(text[s.lo:s.hi]) {
				t.Fatalf("%q: passage %v is not valid UTF-8", unit, s)
			}
		}
	}
}

// TestSplitShortAndEmpty: a short text is one passage, the whole of it; text with
// nothing but space has none.
func TestSplitShortAndEmpty(t *testing.T) {
	if sp := split("Title\n\nOne line."); len(sp) != 1 || sp[0] != (span{0, 16}) {
		t.Fatalf("short text: %v", sp)
	}
	for _, text := range []string{"", " \n\n\t "} {
		if sp := split(text); sp != nil {
			t.Fatalf("%q: %v", text, sp)
		}
	}
}
