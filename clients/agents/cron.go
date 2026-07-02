package agents

// A minimal, dependency-free 5-field cron matcher — the ONE schedule grammar
// long-running agents use. It is deliberately tiny (no seconds field, no
// @-macros, no timezones beyond UTC) because the scheduler ticks once a minute
// and only needs to answer "does this expression fire at this minute?".
//
// Grammar (standard 5-field, all times UTC):
//
//	minute hour day-of-month month day-of-week
//	 0-59   0-23    1-31      1-12     0-6 (Sun=0)
//
// Each field is a comma list of terms; a term is "*", a number, a range "a-b",
// or a step "*/n" or "a-b/n". Day-of-month and day-of-week combine with OR when
// BOTH are restricted (Vixie-cron semantics), else AND — matching what operators
// expect from "0 9 * * 1" (09:00 on Mondays).
//
// Hand-rolled instead of pulling a cron module: it is ~one screen, fully
// unit-tested, and keeps the dependency surface minimal (no new module in a
// binary that mounts every subsystem).

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schedule is a parsed cron expression: one bitset per field (bit i set => the
// field matches value i). domRestricted/dowRestricted record whether the
// day-of-month / day-of-week field was anything other than "*", which selects
// the OR-vs-AND combination rule.
type schedule struct {
	min, hour, dom, mon, dow uint64
	domRestricted            bool
	dowRestricted            bool
}

// fieldRange bounds each cron field (inclusive).
type fieldRange struct{ min, max int }

var cronRanges = [5]fieldRange{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week (Sunday=0)
}

// parseCron parses a 5-field cron expression or returns an error describing the
// first malformed field. Whitespace between fields is collapsed.
func parseCron(expr string) (schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return schedule{}, fmt.Errorf("cron: want 5 fields, got %d in %q", len(fields), expr)
	}
	var s schedule
	dst := []*uint64{&s.min, &s.hour, &s.dom, &s.mon, &s.dow}
	for i, f := range fields {
		bits, err := parseField(f, cronRanges[i])
		if err != nil {
			return schedule{}, fmt.Errorf("cron field %d (%q): %w", i+1, f, err)
		}
		*dst[i] = bits
	}
	s.domRestricted = fields[2] != "*"
	s.dowRestricted = fields[4] != "*"
	return s, nil
}

// parseField parses one comma-separated cron field into a bitset over r.
func parseField(f string, r fieldRange) (uint64, error) {
	if f == "" {
		return 0, fmt.Errorf("empty field")
	}
	var bits uint64
	for _, term := range strings.Split(f, ",") {
		tb, err := parseTerm(term, r)
		if err != nil {
			return 0, err
		}
		bits |= tb
	}
	return bits, nil
}

// parseTerm parses a single term: "*", "n", "a-b", "*/n", or "a-b/n".
func parseTerm(term string, r fieldRange) (uint64, error) {
	step := 1
	if i := strings.IndexByte(term, '/'); i >= 0 {
		n, err := strconv.Atoi(term[i+1:])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad step %q", term)
		}
		step = n
		term = term[:i]
	}

	lo, hi := r.min, r.max
	switch {
	case term == "*":
		// full range with the parsed step.
	case strings.IndexByte(term, '-') > 0:
		i := strings.IndexByte(term, '-')
		a, err1 := strconv.Atoi(term[:i])
		b, err2 := strconv.Atoi(term[i+1:])
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("bad range %q", term)
		}
		lo, hi = a, b
	default:
		n, err := strconv.Atoi(term)
		if err != nil {
			return 0, fmt.Errorf("bad number %q", term)
		}
		lo, hi = n, n
	}

	if lo < r.min || hi > r.max || lo > hi {
		return 0, fmt.Errorf("value out of range [%d,%d]", r.min, r.max)
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

// matches reports whether the schedule fires at t (evaluated in UTC, minute
// granularity). The day-of-month / day-of-week combination follows Vixie cron:
// when BOTH are restricted the day matches if EITHER matches (OR); otherwise the
// unrestricted field is a wildcard and the restricted one is ANDed.
func (s schedule) matches(t time.Time) bool {
	t = t.UTC()
	if s.min&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if s.mon&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domHit := s.dom&(1<<uint(t.Day())) != 0
	dowHit := s.dow&(1<<uint(int(t.Weekday()))) != 0
	if s.domRestricted && s.dowRestricted {
		return domHit || dowHit
	}
	return domHit && dowHit
}
