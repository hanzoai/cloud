package knowledge

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// split.go cuts a document's text into the passages the vector leg embeds.
//
// A passage gets a vector, never a whole document: an embedding model reads a
// bounded prefix and averages what it reads, so one vector for a long page leaves
// a fact in its tail past the prefix or diluted into the page's gist, and a
// question about it finds nothing. A passage is small enough that the fact IS its
// gist.
//
// github.com/hanzoai/semantic carries a split package from v0.2.0; this module
// pins v0.1.0, which has none, so this is the minimal splitter the vector leg
// needs rather than a dependency bump that moves every other user of that module.
//
// It cuts only where Markdown already breaks: before a heading, at a blank line,
// never inside a ``` fence. Those blocks are packed into passages of at most
// chunkSize bytes; a heading starts a new passage unless its whole section still
// fits in the current one, so a section is not spread over two passages when it
// could have had one. A block longer than chunkSize is cut at the last line
// break, sentence end or space inside the bound — at a rune boundary, whatever
// happens. Each passage after the first reaches back up to chunkOverlap bytes, to
// the start of a word, so a sentence cut in two is whole in one of the two
// passages.
//
// Sizes are BYTES: a span is a byte range of the text, and a passage of multi-byte
// script is correspondingly fewer characters, which errs toward what an embedding
// model's token window holds.

const (
	chunkSize    = 2000
	chunkOverlap = 200
)

// span is a passage: the half-open byte range [lo, hi) of the document's text.
type span struct{ lo, hi int }

// split returns the passages of text in order. Without overlap they tile it: the
// passages concatenated are the text, byte for byte. Empty text has none.
func split(text string) []span {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var tiles []span
	cur := span{}
	bs := blocks(text)
	for i, b := range bs {
		// A heading asks for room for its whole section, up to the next heading.
		end := b.hi
		if b.head {
			for j := i + 1; j < len(bs) && !bs[j].head; j++ {
				end = bs[j].hi
			}
		}
		switch {
		case end-cur.lo <= chunkSize:
			cur.hi = b.hi
		case cur.hi > cur.lo:
			tiles = append(tiles, cur)
			cur = span{lo: cur.hi, hi: b.hi}
		default:
			cur.hi = b.hi
		}
		for cur.hi-cur.lo > chunkSize {
			cut := cutPoint(text, cur.lo, cur.lo+chunkSize)
			tiles = append(tiles, span{cur.lo, cut})
			cur.lo = cut
		}
	}
	if cur.hi > cur.lo {
		tiles = append(tiles, cur)
	}
	out := make([]span, 0, len(tiles))
	for i, t := range tiles {
		if i > 0 {
			t.lo = reach(text, max(tiles[i-1].lo, t.lo-chunkOverlap), t.lo)
		}
		if strings.TrimSpace(text[t.lo:t.hi]) != "" {
			out = append(out, t)
		}
	}
	return out
}

// block is a Markdown block: its span, and whether it opens with a heading.
type block struct {
	span
	head bool
}

// blocks tiles text into Markdown blocks: a new block starts at a heading line,
// at an opening ``` or ~~~ fence, and at the first line after a blank line or a
// closing fence. A fence is one block however long. The blocks cover every byte,
// so packing them loses none.
func blocks(text string) []block {
	var out []block
	start, fenced, blank, head := 0, false, false, false
	for lo := 0; lo < len(text); {
		hi := strings.IndexByte(text[lo:], '\n') + 1
		if hi == 0 {
			hi = len(text)
		} else {
			hi += lo
		}
		line := strings.TrimSpace(text[lo:hi])
		fence := strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
		h := !fenced && heading(line)
		if lo == start {
			head = h
		} else if !fenced && (blank && line != "" || h || fence) {
			out = append(out, block{span{start, lo}, head})
			start, head = lo, h
		}
		if fence {
			fenced = !fenced
		}
		// A closing fence ends its block as a blank line would.
		blank = line == "" || fence && !fenced
		lo = hi
	}
	return append(out, block{span{start, len(text)}, head})
}

// heading reports an ATX heading line: one to six '#' and then a space or nothing.
func heading(line string) bool {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && (n == len(line) || line[n] == ' ' || line[n] == '\t')
}

// cutPoint is where a too-long run [lo, bound) is cut: after its last line break,
// else after its last sentence end, else after its last space — in the second
// half of the run, so a passage is never a sliver — else at the bound, stepped
// back to a rune boundary.
func cutPoint(text string, lo, bound int) int {
	run := text[lo:bound]
	half := len(run) / 2
	if i := strings.LastIndexByte(run, '\n'); i >= half {
		return lo + i + 1
	}
	for _, end := range []string{". ", "! ", "? ", "。"} {
		if i := strings.LastIndex(run, end); i >= half {
			return lo + i + len(end)
		}
	}
	if i := strings.LastIndexFunc(run, unicode.IsSpace); i >= half {
		_, n := utf8.DecodeRuneInString(run[i:])
		return lo + i + n
	}
	for bound > lo+1 && !utf8.RuneStart(text[bound]) {
		bound--
	}
	return bound
}

// reach moves a passage's start back from lo toward from, to the first word that
// begins at or after from — so the overlap never opens mid-word. With no word
// start in reach, the passage keeps lo.
func reach(text string, from, lo int) int {
	for i := from; i < lo; i++ {
		if !utf8.RuneStart(text[i]) {
			continue
		}
		prev, _ := utf8.DecodeLastRuneInString(text[:i])
		cur, _ := utf8.DecodeRuneInString(text[i:])
		if (i == 0 || unicode.IsSpace(prev)) && !unicode.IsSpace(cur) {
			return i
		}
	}
	return lo
}
