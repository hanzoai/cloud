package coding

import (
	"strings"
)

// normalizeApostrophes replaces typographic apostrophes and quotes with ASCII
// equivalents before matching. The harness uses U+2019 (’) in contractions like
// "I'll" / "I'm" and U+2018/U+201C/U+201D in quoted text; the patterns in
// narrating() and settled() are ASCII so they never match without this step.
func normalizeApostrophes(s string) string {
	return strings.NewReplacer(
		"\u2019", "'", // right single quotation mark (typographic apostrophe)
		"\u2018", "'", // left single quotation mark
		"\u201c", "\"", // left double quotation mark
		"\u201d", "\"", // right double quotation mark
		"\u02bc", "'", // modifier letter apostrophe (ʼ)
		"\uff07", "'", // fullwidth apostrophe (＇)
	).Replace(s)
}

// maxUnacted is the number of consecutive message-only check turns the settle
// loop will tolerate while the run is still Unacted (no change, not settled).
// After this many, the run is marked Unacted with an error. This is a constant
// so parallel PRs can adjust it without touching the loop logic.
const maxUnacted = 3
