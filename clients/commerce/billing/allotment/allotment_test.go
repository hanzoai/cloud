package allotment

import (
	"testing"
	"time"
)

func TestPeriod(t *testing.T) {
	at := time.Date(2026, 6, 19, 14, 30, 0, 0, time.UTC)
	if got := Period(at); got != "2026-06" {
		t.Errorf("Period = %q, want 2026-06", got)
	}
	// Non-UTC input is normalized to UTC before formatting.
	loc := time.FixedZone("UTC-5", -5*3600)
	// 2026-07-01 02:00 -05:00 == 2026-07-01 07:00 UTC -> still July.
	jul := time.Date(2026, 7, 1, 2, 0, 0, 0, loc)
	if got := Period(jul); got != "2026-07" {
		t.Errorf("Period(non-UTC) = %q, want 2026-07", got)
	}
}

func TestTag(t *testing.T) {
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	want := "included-credit:2026-06"
	if got := Tag(at); got != want {
		t.Errorf("Tag = %q, want %q", got, want)
	}
}

func TestPeriodEnd(t *testing.T) {
	// Mid-month -> first instant of next month, UTC.
	at := time.Date(2026, 6, 19, 14, 30, 0, 0, time.UTC)
	end := PeriodEnd(at)
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !end.Equal(want) {
		t.Errorf("PeriodEnd = %v, want %v", end, want)
	}
	// The grant must be live for the whole of its own month, and expired at
	// the first instant of the next month (so it does not carry over —
	// TallyTransactions excludes deposits whose ExpiresAt is before 'now').
	if !end.After(at) {
		t.Error("PeriodEnd must be after the grant time")
	}

	// December rolls into next January.
	dec := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	if got := PeriodEnd(dec); !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PeriodEnd(Dec) = %v, want 2027-01-01T00:00:00Z", got)
	}
}
