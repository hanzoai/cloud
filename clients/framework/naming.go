package framework

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Naming — the DocType autoname engine, faithful to Frappe's make_autoname.
//
// A DocType's Autoname rule decides a new document's `name` (its business key,
// unique within (org, doctype)):
//
//   - ""     or "hash"        → a random, collision-resistant id (the default).
//   - "field:fieldname"       → the value of that field (must be non-empty; the
//                               field is typically Unique/Data).
//   - "prompt"                → the client supplies `name` in the request body.
//   - anything else           → a SERIES pattern (see expandSeries): dot-delimited
//                               tokens with date parts (YYYY/YY/MM/DD) and a run of
//                               '#' marking a zero-padded per-org counter, e.g.
//                               "INV-.YYYY.-.#####" → "INV-2026-00001".
//
// The counter is per (org, seriesKey), incremented atomically inside the create
// transaction (see Store.next), so two concurrent creates in the same org never
// collide on a series name.

// autonameField returns the field name for a "field:xxx" rule, else "".
func autonameField(autoname string) string {
	autoname = strings.TrimSpace(autoname)
	if rest, ok := strings.CutPrefix(autoname, "field:"); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

// isPromptNaming reports whether the client must supply the name.
func isPromptNaming(autoname string) bool {
	return strings.EqualFold(strings.TrimSpace(autoname), "prompt")
}

// isHashNaming reports whether names are random hashes (the default).
func isHashNaming(autoname string) bool {
	a := strings.TrimSpace(autoname)
	return a == "" || strings.EqualFold(a, "hash")
}

// isSeriesNaming reports whether autoname is a series pattern (not hash / field /
// prompt). Such patterns drive expandSeries + a per-org counter.
func isSeriesNaming(autoname string) bool {
	a := strings.TrimSpace(autoname)
	return a != "" && !isHashNaming(a) && !isPromptNaming(a) && autonameField(a) == ""
}

// hashName returns a random 128-bit lowercase-hex id — the default document
// name. Collision probability across a tenant is negligible.
func hashName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// series is the parsed form of a naming pattern: the invariant Key (per which the
// counter increments, per org), the Prefix/Suffix around the counter, and the
// zero-pad Digits.
type series struct {
	Key    string
	Prefix string
	Suffix string
	Digits int
}

// expandSeries parses a naming pattern into a series, expanding date tokens
// against now. Tokens are dot-delimited (Frappe convention): "YYYY"/"YY"/"MM"/
// "DD" become the corresponding date fields, a run of '#' marks the counter
// (its length is the zero-pad width), and any other segment is a literal. Dots
// are delimiters, not literals. A pattern with no '#' run gets a default 5-digit
// counter appended (Frappe semantics: "MYDOC-" → "MYDOC-00001").
func expandSeries(pattern string, now time.Time) series {
	parts := strings.Split(pattern, ".")
	var prefix, suffix strings.Builder
	digits := 0
	seenCounter := false
	for _, p := range parts {
		switch {
		case p == "":
			// A "." delimiter with nothing between; contributes no literal.
			continue
		case isHashRun(p):
			digits = len(p)
			seenCounter = true
			continue
		}
		tok := expandDateToken(p, now)
		if seenCounter {
			suffix.WriteString(tok)
		} else {
			prefix.WriteString(tok)
		}
	}
	if !seenCounter {
		digits = 5 // default width appended at the end
	}
	pfx, sfx := prefix.String(), suffix.String()
	return series{Key: pfx + "\x00" + sfx, Prefix: pfx, Suffix: sfx, Digits: digits}
}

// format renders the document name for counter n.
func (s series) format(n int64) string {
	return s.Prefix + fmt.Sprintf("%0*d", s.Digits, n) + s.Suffix
}

// isHashRun reports whether s is a non-empty run of '#'.
func isHashRun(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '#' {
			return false
		}
	}
	return true
}

// expandDateToken replaces a known date token with its value derived from now,
// else returns the segment as a literal.
func expandDateToken(seg string, now time.Time) string {
	switch seg {
	case "YYYY":
		return fmt.Sprintf("%04d", now.Year())
	case "YY":
		return fmt.Sprintf("%02d", now.Year()%100)
	case "MM":
		return fmt.Sprintf("%02d", int(now.Month()))
	case "DD":
		return fmt.Sprintf("%02d", now.Day())
	default:
		return seg
	}
}
