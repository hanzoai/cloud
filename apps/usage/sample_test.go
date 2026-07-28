package usage

import (
	"math"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

// TestWindowInstanceIsStableAcrossRepolls is THE idempotency gate at the value
// layer: a poller re-reading the SAME live window must land on the SAME instance
// every time, because that instant is the dedup key. If it drifted, each poll would
// key a new row, nothing would ever collapse, and every sum would be multiplied by
// the poll rate.
//
// A snapshot is cumulative-to-date, not a delta: these three reports say "42% …
// 47% … 51% OF THE SAME 6h WINDOW", and they must be ONE row.
func TestWindowInstanceIsStableAcrossRepolls(t *testing.T) {
	resets := now.Add(90 * time.Minute) // the provider's own boundary
	var keys []time.Time
	for i, at := range []time.Time{now, now.Add(30 * time.Second), now.Add(4 * time.Minute)} {
		s := Sample{
			Provider: "claude", Machine: "m1", Window: Window6h, Lane: "five_hour",
			WindowMinutes: 300, ResetsAt: resets, UsedPct: float64(42 + i*5),
		}.Sanitize(at)
		keys = append(keys, s.WindowStart)
	}
	for i := 1; i < len(keys); i++ {
		if !keys[i].Equal(keys[0]) {
			t.Fatalf("re-poll %d keyed a DIFFERENT instance (%s vs %s) — every sum would double-count",
				i, keys[i], keys[0])
		}
	}
	// And it is the provider's boundary: resets_at − the window's length.
	if want := resets.Add(-300 * time.Minute); !keys[0].Equal(want) {
		t.Fatalf("instance = %s, want resets_at−window = %s", keys[0], want)
	}
}

// TestIndependentObserversAgree: two machines watching the SAME account must key
// the same window instance, or one plan's quota is stored twice and doubles.
// Neither the reset-derived path nor the grid-bucket fallback may depend on which
// machine observed, or on when within the window it looked.
func TestIndependentObserversAgree(t *testing.T) {
	resets := now.Add(2 * time.Hour)
	laptop := Sample{Provider: "claude", Machine: "laptop", Window: Window6h,
		WindowMinutes: 300, ResetsAt: resets}.Sanitize(now)
	desktop := Sample{Provider: "claude", Machine: "desktop", Window: Window6h,
		WindowMinutes: 300, ResetsAt: resets}.Sanitize(now.Add(3 * time.Minute))
	if !laptop.WindowStart.Equal(desktop.WindowStart) {
		t.Fatalf("two observers of one account keyed different instances: %s vs %s",
			laptop.WindowStart, desktop.WindowStart)
	}

	// The no-reset fallback buckets on a fixed grid, so observers still agree even
	// when the meter reports no boundary at all.
	a := Sample{Provider: "codex", Machine: "a", Window: Window6h, WindowMinutes: 360}.Sanitize(now)
	b := Sample{Provider: "codex", Machine: "b", Window: Window6h, WindowMinutes: 360}.Sanitize(now.Add(7 * time.Minute))
	if !a.WindowStart.Equal(b.WindowStart) {
		t.Fatalf("grid fallback disagreed: %s vs %s", a.WindowStart, b.WindowStart)
	}
}

// TestNewWindowIsANewRow: when a window RESETS, the next instance must key
// differently — dedup must collapse re-reports without collapsing real history.
func TestNewWindowIsANewRow(t *testing.T) {
	first := Sample{Provider: "claude", Machine: "m1", Window: Window6h,
		WindowMinutes: 300, ResetsAt: now.Add(time.Hour)}.Sanitize(now)
	next := Sample{Provider: "claude", Machine: "m1", Window: Window6h,
		WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)}.Sanitize(now.Add(2 * time.Hour))
	if first.WindowStart.Equal(next.WindowStart) {
		t.Fatal("a NEW window instance collapsed onto the previous one — real history would be lost")
	}
}

// TestWindowlessCounterIsOneRow: a counter whose meter reported no duration and no
// reset still keys ONE stable instance per lane — its class's nominal bucket — so
// re-polls of it COLLAPSE onto one row (ReplacingMergeTree by ts) instead of
// stacking a new row per poll and being summed. The instance is never the zero
// instant, which every ranged read filters out (window_start ∈ [from,to)) and the
// TTL drops on arrival — see TestEveryValidWindowKeysARepresentableInstant.
func TestWindowlessCounterIsOneRow(t *testing.T) {
	a := Sample{Provider: "hanzo", Machine: "m1", Window: WindowMonth, TotalTokens: 10}.Sanitize(now)
	b := Sample{Provider: "hanzo", Machine: "m1", Window: WindowMonth, TotalTokens: 20}.Sanitize(now.Add(time.Hour))
	if a.WindowStart.IsZero() || b.WindowStart.IsZero() {
		t.Fatalf("a windowless counter must key a representable instant, got %s / %s", a.WindowStart, b.WindowStart)
	}
	if !a.WindowStart.Equal(b.WindowStart) {
		t.Fatalf("two polls of one windowless counter must key the SAME instance (one row), got %s vs %s",
			a.WindowStart, b.WindowStart)
	}
}

// TestSanitizeBounds: every field is bounded, so a hostile or buggy client can
// never bloat a row, wrap a column, or smuggle a non-finite float.
func TestSanitizeBounds(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	got := Sample{
		Provider: huge, Account: huge, Plan: huge, Machine: huge, Lane: huge, Currency: huge,
		Kind: "wildcard", Confidence: "totally-legit",
		UsedPct:       math.Inf(1),
		WindowMinutes: 1 << 30,
		Requests:      -5, InputTokens: math.MaxInt64, OutputTokens: -1,
		TotalTokens: math.MaxInt64, CachedInputTokens: -99,
		CostCents: math.MaxInt64, CostLimitCents: -1,
	}.Sanitize(now)

	if len(got.Provider) > maxProvider || len(got.Account) > maxAccount ||
		len(got.Plan) > maxPlan || len(got.Machine) > maxMachine ||
		len(got.Lane) > maxLane || len(got.Currency) > maxCurrency {
		t.Fatalf("a string escaped its cap: %+v", got)
	}
	if got.UsedPct != 0 {
		t.Fatalf("+Inf percent must coerce to 0, got %v", got.UsedPct)
	}
	if got.WindowMinutes != maxWindowMinutes {
		t.Fatalf("windowMinutes must clamp to %d, got %d", maxWindowMinutes, got.WindowMinutes)
	}
	for name, v := range map[string]int64{
		"requests": got.Requests, "inputTokens": got.InputTokens, "outputTokens": got.OutputTokens,
		"totalTokens": got.TotalTokens, "cachedInputTokens": got.CachedInputTokens,
		"costCents": got.CostCents, "costLimitCents": got.CostLimitCents,
	} {
		if v < 0 || v > maxCounter {
			t.Fatalf("%s escaped [0,%d]: %d", name, maxCounter, v)
		}
	}
	// An unrecognised kind/confidence must DEFAULT, never pass through — `exact`
	// especially must be earned, since it is what licenses a reader to believe.
	if got.Kind != KindSubscription {
		t.Fatalf("an unknown kind must default to subscription, got %q", got.Kind)
	}
	if got.Confidence != ConfidenceUnknown {
		t.Fatalf("an unknown confidence must default to unknown, got %q", got.Confidence)
	}
}

// TestSanitizePercentIsClamped covers the percent range specifically: it drives a
// headroom ordering, so a nonsense value must never order candidates.
func TestSanitizePercentIsClamped(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-10, 0}, {0, 0}, {47.5, 47.5}, {100, 100}, {1e9, 100},
		{math.NaN(), 0}, {math.Inf(-1), 0},
	} {
		got := Sample{Provider: "claude", Machine: "m", Window: Window6h, UsedPct: tc.in}.Sanitize(now)
		if got.UsedPct != tc.want {
			t.Fatalf("usedPct %v → %v, want %v", tc.in, got.UsedPct, tc.want)
		}
	}
}

// TestClientClockIsBounded: the two instants a client may state are clamped to a
// sane interval, so neither a far-future nor a far-past value is ever stored.
func TestClientClockIsBounded(t *testing.T) {
	far := Sample{Provider: "claude", Machine: "m", Window: Window6h,
		WindowStart: now.AddDate(50, 0, 0)}.Sanitize(now)
	if far.WindowStart.After(now.Add(maxAhead)) {
		t.Fatalf("a far-future windowStart was stored: %s", far.WindowStart)
	}
	old := Sample{Provider: "claude", Machine: "m", Window: Window6h,
		WindowStart: now.AddDate(-50, 0, 0)}.Sanitize(now)
	if old.WindowStart.Before(now.Add(-maxBehind)) {
		t.Fatalf("a far-past windowStart was stored: %s", old.WindowStart)
	}
}

// TestLaneDefaultsToWindow: a reporter that knows only its window class still gets
// a stable, correctly-keyed lane.
func TestLaneDefaultsToWindow(t *testing.T) {
	got := Sample{Provider: "claude", Machine: "m", Window: WindowWeek}.Sanitize(now)
	if got.Lane != WindowWeek {
		t.Fatalf("lane must default to the window class, got %q", got.Lane)
	}
}

// TestCanonicalWindowVocabulary pins the closed vocabulary: exactly four values,
// lowercase, no synonyms and no case variants. This is the value rate limits,
// quotas and usage rollups all share, so drift here is drift everywhere.
func TestCanonicalWindowVocabulary(t *testing.T) {
	for _, ok := range []string{"6h", "day", "week", "month"} {
		if !validWindow(ok) {
			t.Fatalf("%q must be canonical", ok)
		}
	}
	for _, bad := range []string{
		"", "5h", "1h", "hour", "hourly", "daily", "weekly", "monthly", "session", "total",
		"6H", "Day", "Week", "WEEK", "Monthly", "7d", "30d", " day", "day ",
	} {
		if validWindow(bad) {
			t.Fatalf("%q must NOT be a window — the vocabulary is closed", bad)
		}
	}
}
