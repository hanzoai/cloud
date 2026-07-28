package link

import (
	"math"
	"strings"
	"time"
)

// sample.go is the ACCOUNT-USAGE value plane: ONE metering lane of ONE provider
// account, as observed at one instant. It is the atom hanzo.account_usage stores
// and the usage faces read.
//
// IT IS SHAPED TO WHAT @hanzo/usage's METER CAN ACTUALLY REPORT — never more. The
// meter reads each provider's OWN login (no API key) and yields a UsageSnapshot:
// rate-limit lanes carrying a used PERCENT (RateWindow.usedPercent + windowMinutes
// + resetsAt), an OPTIONAL absolute UsageTotals (tokens/requests — present only at
// `exact` confidence), an OPTIONAL ProviderCostSnapshot (used/limit, MONEY), and a
// dataConfidence saying how much of that is real. Claude — the account this plane
// exists for — reports dataConfidence `percentOnly` and NO totals at all: its truth
// is the five_hour / seven_day PERCENT and nothing else. So UsedPct is a
// first-class column here and an absolute token quota is NOT one: no source can
// produce it (neither the meter nor the hanzoai/plans catalog — see the quota gap
// note on UsedPct).
//
// THREE ORTHOGONAL VALUES say "which meter", because they genuinely differ:
//
//   - Window        the CANONICAL class the platform bills and limits on
//   - WindowMinutes the duration the meter ACTUALLY reported — never rounded to fit
//     the class, so the console renders the truth ("5h") not the bucket
//   - Lane          the meter's own lane id — the IDENTITY, and the only value that
//     can key a lane
//
// Claude's five_hour lane is 300 minutes and classes as `6h`. Its seven_day,
// seven_day_sonnet and seven_day_opus lanes ALL class as `week` at 10080 minutes
// yet are THREE DIFFERENT METERS — only Lane tells them apart, so only Lane can key
// them. Collapsing them onto the class would silently overwrite two of the three.
//
// NO SECRET LIVES HERE (the package rule): a sample is counters and percents. The
// provider token never leaves the device.

// The CANONICAL window value: the ONE closed vocabulary that rate limits, quotas,
// and usage rollups all share, platform-wide. Lowercase, no case variants, no
// synonyms — a window is one of exactly these four values.
//
// This declaration is the single source of the vocabulary. (hanzoai/commerce today
// carries an ad-hoc set — weekly/daily/monthly/hourly plus capitalized variants,
// and no sub-day window at all; a later commerce pass adopts THESE values. Do not
// copy the old strings back in.)
const (
	Window6h    = "6h"
	WindowDay   = "day"
	WindowWeek  = "week"
	WindowMonth = "month"
)

// Confidence values — how real a sample's numbers are. These mirror @hanzo/usage's
// UsageDataConfidence EXACTLY (they are the meter's own wire values, already
// carried on Usage.Confidence), so a value crosses the whole system unmapped.
//
// This is the ANTI-FABRICATION FLAG, and the reason it is a column: a Claude
// sample is `percentOnly`, so its token counters are 0 because they are UNKNOWN —
// not because the account consumed nothing. Without this value a reader cannot
// tell "0 tokens" from "no idea", and would render a fabricated zero. With it, the
// console renders "—".
const (
	ConfidenceExact       = "exact"
	ConfidenceEstimated   = "estimated"
	ConfidencePercentOnly = "percentOnly"
	ConfidenceUnknown     = "unknown"
)

// Sources for the global view: which plane a usage row came from. They are NOT
// interchangeable and are never added together —
//
//   - SourceAccount the provider's OWN plan consumption, metered from the user's
//     own login. Its cost is what the PROVIDER says it charged (0 for a flat
//     subscription); its percent is plan quota. NOT a Hanzo charge.
//   - SourceHanzo   hanzo.cloud_usage — Hanzo-routed inference. COST OF RECORD.
const (
	SourceAccount = "account"
	SourceHanzo   = "hanzo"
)

// Scopes for a global-view row: the tenancy the row's numbers cover. The two
// sources answer at different scopes and the row says which, so a reader never
// silently compares a user's plan usage against an org's whole spend.
const (
	ScopeUser = "user" // the caller's OWN linked accounts (org+subject)
	ScopeOrg  = "org"  // the whole org's Hanzo-routed usage
)

// Field bounds. A sample is a handful of numbers plus short identifiers; the caps
// keep a hostile or buggy client from bloating the warehouse. Provider/Account/
// Plan/Machine reuse the Link caps — the same values, one bound each.
const (
	maxLane     = 64
	maxCurrency = 8
	// maxSamples caps ONE report. A machine's meter yields a few lanes per provider
	// (Claude alone has four), and a cost-history backfill adds a row per day, so
	// the cap is generous — but closed.
	maxSamples = 256
	// maxCounter bounds a counter well inside UInt64 and float64's exact-integer
	// range, so a garbage 1e30 clamps instead of wrapping the warehouse column.
	maxCounter = 1 << 52
	// maxWindowMinutes is a year — longer than any real metering window.
	maxWindowMinutes = 366 * 24 * 60
)

// Clock bounds for the two client-supplied instants. The SERVER owns the
// observation clock (Sample.ts) absolutely; these bound only the WINDOW the sample
// describes, which the client legitimately knows (Claude reports resets_at).
const (
	// maxAhead lets a window reset in the future (that is what a reset IS) but not
	// beyond the longest window plus slack — no far-future pinning.
	maxAhead = 400 * 24 * time.Hour
	// maxBehind lets a backfill report a historical window inside the table's own
	// TTL, and no further — no far-past rows that the TTL would drop on arrival.
	maxBehind = 400 * 24 * time.Hour
)

// Sample is one metering lane's consumption of one provider account at one
// observation.
//
// TENANCY IS NOT ON THE VALUE. Org and subject are bound by the server from the
// validated principal at the boundary, so a client cannot assert whose usage this
// is — the shape makes cross-tenant writes unrepresentable rather than merely
// refused.
type Sample struct {
	Provider string // matches Link.Provider / @hanzo/usage providerRegistry id
	Account  string // the account label (an identifier, never a secret)
	Plan     string // the plan name from the provider identity; display only
	Kind     string // subscription | apikey (validKind) — mirrors Link.Kind
	Machine  string // the machine that OBSERVED this (an attribute, not a key)

	Lane          string    // the meter's lane id (five_hour|seven_day_opus|…) — the identity
	Window        string    // the canonical class (6h|day|week|month)
	WindowMinutes int32     // the duration the meter reported (300 for Claude's 5h)
	WindowStart   time.Time // WHICH instance of the window this measures — the dedup key
	ResetsAt      time.Time // when the window resets (RateWindow.resetsAt); zero = unknown

	// UsedPct is the lane's used percent, 0..100 — RateWindow.usedPercent. For a
	// subscription account this is THE quota signal and often the ONLY one.
	//
	// QUOTA GAP (deliberate, reported, not faked): there is NO absolute
	// used/limit token quota column, because nothing can populate one. The meter
	// reports a percent, not a limit (Claude: dataConfidence `percentOnly`). The
	// hanzoai/plans catalog expresses `ai.requests_per_min` / `ai.tokens_per_min`
	// — per-MINUTE rate limits, a different concept from a window quota — and in
	// any case it is HANZO's own plan catalog, which cannot know what Anthropic
	// grants a Claude Max plan. An always-zero quota_limit would read as "the
	// limit is zero"; absent is honest. It lands as an additive column when a
	// source for it exists.
	UsedPct    float64
	Confidence string // exact|estimated|percentOnly|unknown — see the const block
	// Synthetic marks a lane the meter FABRICATED (RateWindow.isSyntheticPlaceholder
	// — set when a provider returned null for a lane and the adapter filled it in).
	// Carried so a made-up lane is never rendered as observed truth.
	Synthetic bool

	// Absolute counters — UsageTotals. Present only when the source really reports
	// them (`exact`); zero otherwise, which Confidence disambiguates.
	Requests          int64
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	CachedInputTokens int64

	// Money — ProviderCostSnapshot, in minor units (cents). CostCents is what the
	// PROVIDER says this account spent (Claude's extra_usage overage; 0 for a flat
	// subscription — the plan is the charge). CostLimitCents is that meter's budget
	// (ProviderCostSnapshot.limit) — a MONEY cap, never a token quota. Neither is a
	// Hanzo charge: this plane holds no metering client.
	CostCents      int64
	CostLimitCents int64
	Currency       string // ProviderCostSnapshot.currencyCode; "" = unknown
}

// validWindow reports whether w is the canonical window vocabulary.
func validWindow(w string) bool {
	switch w {
	case Window6h, WindowDay, WindowWeek, WindowMonth:
		return true
	}
	return false
}

// validConfidence reports whether c is a known meter confidence.
func validConfidence(c string) bool {
	switch c {
	case ConfidenceExact, ConfidenceEstimated, ConfidencePercentOnly, ConfidenceUnknown:
		return true
	}
	return false
}

// confidenceRank orders the confidences by HOW MUCH A READER MAY BELIEVE THEM —
// exact is 0, unknown is the worst. It exists so an aggregate can pick the WEAKEST
// contributing confidence explicitly, by rank.
//
// Ordering strings alphabetically instead would be an accident that happens to
// look right: 'exact' < 'percentOnly' < 'unknown' sorts by severity purely by luck
// of spelling, and the first value that breaks the coincidence (an 'approximate',
// say) would silently start reporting mixed data as trustworthy. Severity is a
// property of the vocabulary, so it is declared once, here.
func confidenceRank(c string) uint8 {
	switch c {
	case ConfidenceExact:
		return 0
	case ConfidenceEstimated:
		return 1
	case ConfidencePercentOnly:
		return 2
	}
	return 3 // unknown, and anything unrecognised
}

// confidenceOfRank is confidenceRank's inverse, for decoding an aggregate.
func confidenceOfRank(r int64) string {
	switch r {
	case 0:
		return ConfidenceExact
	case 1:
		return ConfidenceEstimated
	case 2:
		return ConfidencePercentOnly
	}
	return ConfidenceUnknown
}

// nominalMinutes is a window class's NOMINAL length — a property of the
// vocabulary, not a measurement.
//
// It exists only so that every sample has a derivable instance even when the meter
// reported no duration and no reset: without it such a sample would key the zero
// instant, which is not a representable warehouse timestamp. It is NEVER stored as
// the sample's WindowMinutes, which stays the meter's own value (0 = "the meter did
// not say", rendered as unknown). That keeps the reported truth and the class's
// nominal separate: Claude's 6h-class lane really is 300 minutes, and this function
// must never be allowed to say otherwise.
func nominalMinutes(window string) int32 {
	switch window {
	case Window6h:
		return 6 * 60
	case WindowDay:
		return 24 * 60
	case WindowWeek:
		return 7 * 24 * 60
	case WindowMonth:
		return 30 * 24 * 60
	}
	return 0
}

// Sanitize bounds every field so a warehouse row stays small, finite, and
// well-formed no matter what a client sends: strings trimmed and length-capped,
// counters non-negative and clamped, percents coerced into [0,100] even for
// NaN/Inf, instants bounded to a sane window around `now`, and the two enums
// defaulted rather than trusted. It is TOTAL (never errors), so the write path
// always has a safe value — validation of the CLOSED vocabularies (window, kind)
// is the boundary's job and is a 400, because silently rewriting a caller's window
// class would corrupt their dash.
//
// It does NOT set the observation clock: the server stamps that, so a client can
// never backdate a sample or pin a stale one as newest.
func (s Sample) Sanitize(now time.Time) Sample {
	out := Sample{
		Provider: clampStr(s.Provider, maxProvider),
		Account:  clampStr(s.Account, maxAccount),
		Plan:     clampStr(s.Plan, maxPlan),
		Kind:     clampStr(s.Kind, maxProvider),
		Machine:  clampStr(s.Machine, maxMachine),

		Lane:          clampStr(s.Lane, maxLane),
		Window:        clampStr(s.Window, maxLane),
		WindowMinutes: int32(clampInt64(int64(s.WindowMinutes), maxWindowMinutes)),

		UsedPct:    clampPct(finite(s.UsedPct)),
		Confidence: clampStr(s.Confidence, maxLane),
		Synthetic:  s.Synthetic,

		Requests:          clampInt64(s.Requests, maxCounter),
		InputTokens:       clampInt64(s.InputTokens, maxCounter),
		OutputTokens:      clampInt64(s.OutputTokens, maxCounter),
		TotalTokens:       clampInt64(s.TotalTokens, maxCounter),
		CachedInputTokens: clampInt64(s.CachedInputTokens, maxCounter),

		CostCents:      clampInt64(s.CostCents, maxCounter),
		CostLimitCents: clampInt64(s.CostLimitCents, maxCounter),
		Currency:       clampStr(s.Currency, maxCurrency),
	}
	if !validKind(out.Kind) {
		out.Kind = KindSubscription
	}
	if !validConfidence(out.Confidence) {
		// An unrecognised (or absent) confidence is `unknown` — the honest default.
		// Never `exact`: a client must EARN that word, since it is what licenses a
		// reader to believe the counters.
		out.Confidence = ConfidenceUnknown
	}
	// A lane defaults to its window class: a simple reporter that knows only "my 6h
	// window" still gets a stable, correctly-keyed lane of its own.
	if out.Lane == "" {
		out.Lane = out.Window
	}
	out.ResetsAt = boundTime(s.ResetsAt, now)
	// The instance derives from the meter's own duration when it reported one, and
	// from the class's nominal when it did not — so every sample of a valid window
	// has a representable key, while WindowMinutes keeps reporting only what the
	// meter actually said.
	span := out.WindowMinutes
	if span <= 0 {
		span = nominalMinutes(out.Window)
	}
	out.WindowStart = windowInstance(s.WindowStart, out.ResetsAt, span, now)
	return out
}

// windowInstance resolves WHICH instance of the window a sample measures. It is
// THE IDEMPOTENCY KEY: two reports of the same live window must land on the same
// instant, or the same consumption is stored twice and every sum double-counts.
//
// A snapshot is CUMULATIVE-TO-DATE within its window, not a delta — a poller
// re-reading its 6h lane every 30s reports "47%… 48%… 51%" OF THE SAME WINDOW. So
// the row is keyed by the window instance and REPLACED as it grows, never appended.
// Keying on the observation time (the obvious move) would defeat this completely:
// every poll carries a fresh instant, so every poll would key a new row and nothing
// would ever collapse.
//
// Precedence, most authoritative first:
//
//  1. an explicit start — the meter's own instance id, already bounded
//  2. resets_at − the window's length — the provider's own instance boundary
//     (Claude reports resets_at, and every machine watching that account reads the
//     SAME value, so independent observers agree without coordination)
//  3. the observation floored to a fixed grid of the window's length — for a meter
//     that reports no reset, this is deterministic, so independent observers still
//     agree
//  4. the zero instant — a windowless lifetime counter is ONE row per lane,
//     forever replaced, never summed across polls
//
// It is pure: `now` is injected, so the test is deterministic.
func windowInstance(start, resetsAt time.Time, windowMinutes int32, now time.Time) time.Time {
	if !start.IsZero() {
		return boundTime(start, now).Truncate(time.Second)
	}
	d := time.Duration(windowMinutes) * time.Minute
	if !resetsAt.IsZero() && d > 0 {
		return resetsAt.Add(-d).Truncate(time.Second)
	}
	if d > 0 {
		return now.UTC().Truncate(d)
	}
	return time.Time{}
}

// boundTime clamps a client-supplied instant into [now-maxBehind, now+maxAhead] and
// normalises it to UTC, so neither a far-future nor a far-past value can be stored.
// The zero instant means "absent" and stays zero.
func boundTime(t, now time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	t = t.UTC()
	if lo := now.UTC().Add(-maxBehind); t.Before(lo) {
		return lo
	}
	if hi := now.UTC().Add(maxAhead); t.After(hi) {
		return hi
	}
	return t
}

// finite maps NaN/Inf to 0 so a hostile percent can never reach the warehouse or
// break a JSON encode. (clampPct alone cannot: NaN fails every comparison and
// would pass straight through.)
func finite(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// clampInt64 returns a non-negative int64 no larger than hi.
func clampInt64(v, hi int64) int64 {
	if v < 0 {
		return 0
	}
	if v > hi {
		return hi
	}
	return v
}

// clampStr trims a string and caps its length, keeping the result valid UTF-8 so a
// multi-byte rune cut at the cap can never yield a broken column value.
func clampStr(s string, n int) string {
	s = trim(s)
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}
