// Package shorten bounds the length of a string.
package shorten

import (
	"strings"
	"unicode/utf8"
)

// To returns s cut to at most n bytes, never in the middle of a character.
//
// A cut at an arbitrary byte offset splits a multi-byte rune, and the broken half
// travels into whatever the result is written to — a JSON field, a log line, a
// stored sample. Most of the truncators in this repo cut on the offset alone; six
// had already met the problem and repaired it locally, in three different ways.
// This is the repair, in one place, before the damage.
//
// The result is never longer than n and never shorter than the last whole
// character that fits, so a caller's bound still holds exactly.
func To(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Trim is To over a value whose surrounding space was never part of it — the
// bound eleven packages actually wanted when they wrote their own.
//
// THE ORDER IS THE POINT, and it is where two of them differed. Trimming AFTER
// the cut spends the bound on space and then throws the space away, so the
// result is shorter than asked by however much padding the input carried: at
// n=4, " abcdef" yields "abc" that way and "abcd" this way. A bound that moves
// with the input's whitespace is not a bound.
//
// It does NOT mark what it removed. These bound values that are stored, indexed
// and logged, and an ellipsis appended to one of those is a character the value
// never had. A surface that wants the reader to see a cut says so itself.
func Trim(s string, n int) string { return To(strings.TrimSpace(s), n) }
