package team

// rank.go orders siblings the way the Team client does: by a LexoRank string
// ("0|hzzzzz:"), compared as a plain string.
//
// The client parses every rank it reads (makeRank → LexoRank.parse) to place the
// next sibling, so a rank written here must be a VALID LexoRank, not merely one
// that sorts. This writes the one form the client itself writes when it appends:
// a bucket, a six-digit base-36 integer, a colon — middle() for a first child,
// genNext() (ceil, then +8) after the last. Positioning between two siblings is
// the client's own drag-and-drop and is not offered here.

import (
	"strconv"
	"strings"
)

// rankMiddle is LexoRank.middle(): the rank of a first child.
const rankMiddle = "0|hzzzzz:"

// rankStep is how far genNext moves past the ceiling of the last rank.
const rankStep = 8

// rankWidth is the integer part's width in base-36 digits.
const rankWidth = 6

// rankAfter answers the rank that sorts after `last`, as LexoRank.genNext does.
// An empty or unreadable last answers the middle rank, which is what the client
// does for a parent with no children.
func rankAfter(last string) string {
	bucket, rest, ok := strings.Cut(last, "|")
	if !ok || len(bucket) != 1 {
		return rankMiddle
	}
	whole, frac, ok := strings.Cut(rest, ":")
	if !ok || len(whole) != rankWidth {
		return rankMiddle
	}
	n, err := strconv.ParseUint(whole, 36, 64)
	if err != nil {
		return rankMiddle
	}
	// ceil: a fractional part that is not all zeros rounds the integer up.
	if strings.Trim(frac, "0") != "" {
		n++
	}
	n += rankStep
	next := strconv.FormatUint(n, 36)
	if len(next) > rankWidth {
		// Past zzzzzz. LexoRank would bisect toward the maximum; from the middle,
		// six base-36 digits leave over a hundred million appends at a step of
		// eight, so a parent that reaches this has stopped being a document tree,
		// and appending at the last rank again (ties then order by id) keeps the
		// order total rather than inventing a second encoding.
		return last
	}
	return bucket + "|" + strings.Repeat("0", rankWidth-len(next)) + next + ":"
}
