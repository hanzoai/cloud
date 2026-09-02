// Package environ reads settings out of the process environment.
//
// A LEAF, for the reason internal/mint is one: several of the packages root cloud
// imports read the environment too, and they cannot import cloud back.
package environ

import (
	"os"
	"strconv"
	"strings"
)

// Or returns the variable named by key, or def when it is unset or blank.
//
// BLANK MEANS BLANK. A variable holding only spaces is not a value, and twenty-five
// packages disagreed about that — ten read it raw, so CLOUD_BRAND="   " named a
// brand of three spaces, and the blank travelled into a chain name and a model
// name before anything noticed. Trimming here is what makes "set" mean the same
// thing everywhere.
//
// The value is returned TRIMMED, since the padding was never part of it.
func Or(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Int returns the variable named by key as a positive integer, or def when it is
// unset, blank, unparseable, or not positive.
//
// A NON-POSITIVE VALUE TAKES THE DEFAULT, which is the whole reason this is one
// function. These bound something — a pool size, a page cap, a worker count — and
// a bound of zero is not a smaller bound, it is the absence of one. Eleven copies
// of this existed and two accepted any parseable int, so a typo could silently
// uncap what the variable was there to cap.
func Int(key string, def int) int {
	n, err := strconv.Atoi(Or(key, ""))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// Cents returns the variable named by key as a non-negative minor-unit amount,
// and whether it was stated at all.
//
// The bool is the point: an amount has no sensible default, so a caller must be
// able to tell "not configured" from "configured as zero" — a free tier and an
// unpriced one are different answers. Unset, blank, unparseable or negative all
// answer (0, false).
func Cents(key string) (int64, bool) {
	n, err := strconv.ParseInt(Or(key, ""), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
