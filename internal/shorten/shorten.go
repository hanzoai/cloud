// Package shorten bounds the length of a string.
package shorten

import "unicode/utf8"

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
