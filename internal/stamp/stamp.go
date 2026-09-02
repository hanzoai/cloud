// Package stamp renders an instant as RFC 3339, and an absent one as nothing.
package stamp

import "time"

// Unix renders a unix second as RFC 3339 in UTC. A non-positive second is not a
// time — it is the zero a row carries when nothing set it — and renders "".
//
// THE GUARD IS THE WHOLE POINT. Fourteen packages wrote this, and one of them
// omitted it: an unset timestamp reached the wire as "1970-01-01T00:00:00Z" there
// and as "" everywhere else, so one field answered a date that never happened
// while its neighbours answered honestly. A caller cannot tell those apart, and
// a client that parses the first has a valid, wrong instant.
//
// Negative is covered as well as zero: a stored second below the epoch is as
// unset as a zero, and reading it as 1969 is the same lie one year earlier.
func Unix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// At renders a time.Time as RFC 3339 in UTC, and the zero time as "".
//
// The same rule as Unix on the other carrier, for the same reason the identity
// boundary publishes both an Org and an OrgFrom: some callers hold the instant and
// some hold the second, and one of them having its own spelling of "absent" is how
// the two come to disagree.
func At(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
