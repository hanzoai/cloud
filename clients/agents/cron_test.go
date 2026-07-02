package agents

import (
	"testing"
	"time"
)

func TestParseCronErrors(t *testing.T) {
	bad := []string{
		"",             // empty
		"* * * *",      // 4 fields
		"* * * * * *",  // 6 fields
		"60 * * * *",   // minute out of range
		"* 24 * * *",   // hour out of range
		"* * 0 * *",    // dom below range
		"* * 32 * *",   // dom above range
		"* * * 13 *",   // month above range
		"* * * * 7",    // dow above range
		"*/0 * * * *",  // zero step
		"5-1 * * * *",  // inverted range
		"abc * * * *",  // non-numeric
		"1,,2 * * * *", // empty term
	}
	for _, expr := range bad {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) = nil error, want error", expr)
		}
	}
}

func TestParseCronValid(t *testing.T) {
	for _, expr := range []string{
		"* * * * *", "*/5 * * * *", "0 9 * * 1", "0 0 1 * *",
		"0,30 * * * *", "0-15 * * * *", "0 9-17/2 * * 1-5", "0 0 * * 0",
	} {
		if _, err := parseCron(expr); err != nil {
			t.Errorf("parseCron(%q) unexpected error: %v", expr, err)
		}
	}
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04 MST", s+" UTC")
	if err != nil {
		t.Fatalf("bad test time %q: %v", s, err)
	}
	return tm
}

func TestCronMatches(t *testing.T) {
	cases := []struct {
		expr string
		when string // "YYYY-MM-DD HH:MM"
		want bool
	}{
		{"* * * * *", "2026-07-01 12:34", true},
		{"*/5 * * * *", "2026-07-01 12:35", true},
		{"*/5 * * * *", "2026-07-01 12:36", false},
		{"0 9 * * *", "2026-07-01 09:00", true},
		{"0 9 * * *", "2026-07-01 09:01", false},
		{"0 9 * * *", "2026-07-01 10:00", false},
		// 2026-07-06 is a Monday; 0 9 * * 1 fires 09:00 Mondays.
		{"0 9 * * 1", "2026-07-06 09:00", true},
		{"0 9 * * 1", "2026-07-07 09:00", false}, // Tuesday
		{"0-15 * * * *", "2026-07-01 12:15", true},
		{"0-15 * * * *", "2026-07-01 12:16", false},
		{"0 0 1 * *", "2026-08-01 00:00", true},  // first of month
		{"0 0 1 * *", "2026-08-02 00:00", false}, // second of month
		{"0 9-17/2 * * *", "2026-07-01 09:00", true},
		{"0 9-17/2 * * *", "2026-07-01 11:00", true},
		{"0 9-17/2 * * *", "2026-07-01 10:00", false}, // 10 not in 9,11,13,15,17
	}
	for _, c := range cases {
		s, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", c.expr, err)
		}
		if got := s.matches(at(t, c.when)); got != c.want {
			t.Errorf("%q matches %q = %v, want %v", c.expr, c.when, got, c.want)
		}
	}
}

// TestCronDOMDOWOrSemantics: when BOTH day-of-month and day-of-week are
// restricted, Vixie cron fires if EITHER matches. "0 0 13 * 5" fires on the
// 13th OR on any Friday.
func TestCronDOMDOWOrSemantics(t *testing.T) {
	s, err := parseCron("0 0 13 * 5") // 5 = Friday
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2026-07-13 is a Monday -> matches via DOM (the 13th).
	if !s.matches(at(t, "2026-07-13 00:00")) {
		t.Error("should fire on the 13th regardless of weekday")
	}
	// 2026-07-03 is a Friday -> matches via DOW.
	if !s.matches(at(t, "2026-07-03 00:00")) {
		t.Error("should fire on a Friday regardless of day-of-month")
	}
	// 2026-07-06 is a Monday, not the 13th -> no match.
	if s.matches(at(t, "2026-07-06 00:00")) {
		t.Error("must NOT fire on a non-13th non-Friday")
	}
}
