package usage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	aiobject "github.com/hanzoai/ai/object"
)

// datastore.go is the WAREHOUSE projection of account usage: the hanzo.account_usage
// time series, its daily rollup, and the org-scoped reads over them. It moved here
// from clients/link with the rest of the usage plane so the ONE usage subsystem owns
// ALL usage.
//
// WHY A SECOND SOURCE. hanzo.cloud_usage already records every HANZO-ROUTED
// inference (our gateway's own ledger, cost of record — the same llmTable the
// summary and analytics faces read). This table records something different in
// kind: what a user's OWN provider account has consumed of its OWN plan, metered by
// @hanzo/usage reading that provider's own login. Nobody else can know that a Claude
// Max plan is 47% through its 6h window — no Hanzo request produced it. The two are
// never conflated: they are separate tables, read separately, and labelled by source
// wherever they appear together.
//
// FAIL-SOFT IS THE CONTRACT. Every function here degrades to a no-op (writes) or an
// honest "unavailable" (reads) when the datastore is absent — never an error that
// reaches a caller, never a fabricated zero that reads as truth.
//
// TENANCY. Every statement leads with `org = ? AND subject = ?` as BOUND
// parameters. No value a client controls is ever concatenated into SQL.

// warehouse is the account-usage datastore projection. It holds ONLY the
// idempotent-DDL latch; the connection itself is aiobject's (a package global), so
// the warehouse owns no closable handle and the usage subsystem needs no Shutdown.
// dsReady latches the DDL on success only, so a datastore still connecting at boot
// is retried on the next call rather than permanently written off.
type warehouse struct {
	dsMu    sync.Mutex
	dsReady bool
}

const (
	accountUsageTable = "hanzo.account_usage"
	accountUsageDaily = "hanzo.account_usage_daily"
	accountUsageMV    = "hanzo.account_usage_daily_mv"
)

// accountUsageDDL is the ONE definition of the account-usage schema: the base
// series, the daily rollup target, and the materialized view that fills it.
//
// ENGINE — ReplacingMergeTree(ts), NOT MergeTree. A poller re-reports the SAME
// window as it fills (see windowInstance); those reports are the same fact
// observed twice, so the table must COLLAPSE them, not stack them. `ts` (the
// server's observation clock) is the version: the newest read of an instance wins.
//
// ORDER BY IS THE DEDUP KEY, and every part of it earns its place:
//
//	org, subject   the tenant and the owner — isolation is a prefix scan, and a
//	               co-tenant's report can never replace this user's row
//	provider,
//	account        the account being metered
//	lane           the meter's identity. Claude's seven_day, seven_day_sonnet and
//	               seven_day_opus are three different meters at the same 10080
//	               minutes; without `lane` two of the three silently overwrite
//	               the first
//	window_start   WHICH instance of the window — so a re-report replaces, and a
//	               NEW window appends
//
// `ts` is deliberately NOT in the key: it is the version. Keying on it would give
// every poll a distinct key, collapse nothing, and multiply every sum by the poll
// rate — the exact bug this engine is chosen to prevent.
//
// `machine` is deliberately NOT in the key either: a plan's quota belongs to the
// ACCOUNT, not the device. A laptop and a desktop watching the same Claude account
// observe the SAME window; keying by machine would store that one fact twice and
// double every total. The column stays as an attribute — who observed it last.
//
// PARTITION BY window_start, not ts — because ReplacingMergeTree only ever
// collapses rows WITHIN a partition. Partitioning by the observation clock would
// put a re-report of last month's window in a different partition from the
// original, where it could never collapse: the duplicate would be permanent.
// Partitioning by the key's own instant keeps every report of an instance together
// for the life of the row.
var accountUsageDDL = []string{
	`CREATE TABLE IF NOT EXISTS ` + accountUsageTable + ` (
  org                 LowCardinality(String),
  subject             String,
  provider            LowCardinality(String),
  account             String,
  plan                LowCardinality(String),
  kind                LowCardinality(String),
  lane                LowCardinality(String),
  ` + "`window`" + `              LowCardinality(String),
  window_minutes      UInt32,
  window_start        DateTime64(3, 'UTC'),
  resets_at           DateTime64(3, 'UTC'),
  used_pct            Float64,
  requests            UInt64,
  input_tokens        UInt64,
  output_tokens       UInt64,
  total_tokens        UInt64,
  cached_input_tokens UInt64,
  cost_cents          UInt64,
  cost_limit_cents    UInt64,
  currency            LowCardinality(String),
  confidence          LowCardinality(String),
  synthetic           UInt8,
  machine             String,
  ts                  DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY toYYYYMM(window_start)
ORDER BY (org, subject, provider, account, lane, window_start)
TTL toDateTime(window_start) + INTERVAL 1 YEAR`,

	// The daily rollup target. It keeps WINDOW-INSTANCE granularity on purpose —
	// see the MV below for why a coarser key would be wrong — which is still a
	// large rollup: a 30s poller emits ~2880 rows/day per lane and this holds ~4
	// (one per 6h instance).
	`CREATE TABLE IF NOT EXISTS ` + accountUsageDaily + ` (
  org                 LowCardinality(String),
  subject             String,
  provider            LowCardinality(String),
  account             String,
  day                 Date,
  lane                LowCardinality(String),
  ` + "`window`" + `              LowCardinality(String),
  window_start        DateTime64(3, 'UTC'),
  requests            AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  input_tokens        AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  output_tokens       AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  total_tokens        AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  cached_input_tokens AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  cost_cents          AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC')),
  used_pct            AggregateFunction(max, Float64)
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (org, subject, provider, account, day, lane, window_start)
TTL day + INTERVAL 1 YEAR`,

	// The rollup, and the ONE subtlety that decides whether it tells the truth:
	//
	// A materialized view is an INSERT TRIGGER, not a view of the merged table. It
	// sees every raw row as it arrives — including the polls ReplacingMergeTree
	// will later collapse. So `sumState(total_tokens)` here would sum EVERY POLL of
	// the same window and inflate a day's tokens by the poll rate; the base table's
	// dedup could never undo it, because this is a different table. The aggregate
	// must therefore be dedup-PRESERVING:
	//
	//	argMaxState(x, ts)  keeps the newest read of each window instance — the
	//	                    same rule the base table's engine applies, so
	//	                    re-reporting is a no-op here too
	//	maxState(used_pct)  is idempotent by nature (re-reading 47% keeps 47), and
	//	                    over a day of resetting windows it means "peak
	//	                    utilisation that day" — an honest daily metric
	//
	// Summing then happens at READ time, over instances that are disjoint in time.
	`CREATE MATERIALIZED VIEW IF NOT EXISTS ` + accountUsageMV + ` TO ` + accountUsageDaily + ` AS
SELECT
  org,
  subject,
  provider,
  account,
  toDate(window_start) AS day,
  lane,
  ` + "`window`" + `,
  window_start,
  argMaxState(requests, ts)            AS requests,
  argMaxState(input_tokens, ts)        AS input_tokens,
  argMaxState(output_tokens, ts)       AS output_tokens,
  argMaxState(total_tokens, ts)        AS total_tokens,
  argMaxState(cached_input_tokens, ts) AS cached_input_tokens,
  argMaxState(cost_cents, ts)          AS cost_cents,
  maxState(used_pct)                   AS used_pct
FROM ` + accountUsageTable + `
GROUP BY org, subject, provider, account, day, lane, ` + "`window`" + `, window_start`,
}

// ensureAccountUsage creates the account-usage objects idempotently. It latches
// only on success, so a datastore that is still connecting at boot is retried on
// the next call rather than permanently poisoned.
func (w *warehouse) ensureAccountUsage(ctx context.Context) error {
	if !aiobject.DatastoreEnabled() {
		return fmt.Errorf("account usage: datastore not connected")
	}
	w.dsMu.Lock()
	defer w.dsMu.Unlock()
	if w.dsReady {
		return nil
	}
	for _, stmt := range accountUsageDDL {
		if err := aiobject.DatastoreExec(ctx, stmt); err != nil {
			return fmt.Errorf("account usage ddl: %w", err)
		}
	}
	w.dsReady = true
	return nil
}

// accountUsageInsert is the ONE insert statement; its column order is the argument
// order WriteSamples binds.
const accountUsageInsert = `INSERT INTO ` + accountUsageTable + ` (
  org, subject, provider, account, plan, kind, lane, ` + "`window`" + `, window_minutes,
  window_start, resets_at, used_pct, requests, input_tokens, output_tokens,
  total_tokens, cached_input_tokens, cost_cents, cost_limit_cents, currency,
  confidence, synthetic, machine, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// WriteSamples appends the caller's sanitized samples to the warehouse, stamping
// each with the server's observation clock. Org and subject are the SERVER's values
// (from the validated principal), bound positionally — a sample cannot carry them,
// so it cannot forge them.
//
// It is FAIL-SOFT by contract: an absent or blocked datastore is not an error the
// caller must surface to a device — losing a poll of history must never fail a
// report. The record handler turns this error into an honest `stored:false`.
func (w *warehouse) WriteSamples(ctx context.Context, org, subject string, samples []Sample, now time.Time) error {
	if org == "" || subject == "" {
		return fmt.Errorf("account usage: blank tenancy")
	}
	if len(samples) == 0 {
		return nil
	}
	if err := w.ensureAccountUsage(ctx); err != nil {
		return err
	}
	ts := now.UTC()
	for _, x := range samples {
		if err := aiobject.DatastoreExec(ctx, accountUsageInsert,
			org, subject, x.Provider, x.Account, x.Plan, x.Kind, x.Lane, x.Window,
			uint32(x.WindowMinutes), dsInstant(x.WindowStart), dsInstant(x.ResetsAt), x.UsedPct,
			uint64(x.Requests), uint64(x.InputTokens), uint64(x.OutputTokens),
			uint64(x.TotalTokens), uint64(x.CachedInputTokens), uint64(x.CostCents),
			uint64(x.CostLimitCents), x.Currency, x.Confidence, boolBit(x.Synthetic),
			x.Machine, ts); err != nil {
			return fmt.Errorf("account usage insert: %w", err)
		}
	}
	return nil
}

func boolBit(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// ── reads ────────────────────────────────────────────────────────────────────

// dedupedSelect is the inner query EVERY read is built on, and the reason the reads
// are correct.
//
// ReplacingMergeTree collapses duplicates only when parts merge — which happens
// eventually, on the server's own schedule. A read issued before that merge sees
// every poll. So correctness cannot be delegated to the engine: the reader dedups
// EXPLICITLY, with argMax(col, ts) grouped by the same key the engine uses. The
// engine's collapse then only ever saves space; it is never load-bearing for a
// number a user reads.
//
// The GROUP BY is the dedup key EXACTLY — never a superset. Grouping by an
// attribute that a poll can change (a plan rename, a re-classified window) would
// split one instance into two groups and double-count it.
const dedupedSelect = `SELECT
  provider,
  account,
  lane,
  window_start,
  argMax(` + "`window`" + `, ts)         AS window,
  argMax(window_minutes, ts)      AS window_minutes,
  argMax(plan, ts)                AS plan,
  argMax(kind, ts)                AS kind,
  argMax(resets_at, ts)           AS resets_at,
  argMax(used_pct, ts)            AS used_pct,
  argMax(requests, ts)            AS requests,
  argMax(input_tokens, ts)        AS input_tokens,
  argMax(output_tokens, ts)       AS output_tokens,
  argMax(total_tokens, ts)        AS total_tokens,
  argMax(cached_input_tokens, ts) AS cached_input_tokens,
  argMax(cost_cents, ts)          AS cost_cents,
  argMax(cost_limit_cents, ts)    AS cost_limit_cents,
  argMax(currency, ts)            AS currency,
  argMax(confidence, ts)          AS confidence,
  argMax(synthetic, ts)           AS synthetic,
  argMax(machine, ts)             AS machine,
  max(ts)                         AS ts
FROM ` + accountUsageTable + `
WHERE `

const dedupedGroup = `
GROUP BY org, subject, provider, account, lane, window_start`

// maxReadRows caps any one read. A user's lanes over 30 days are a few hundred
// rows; the cap is the backstop that keeps a pathological account from returning
// an unbounded result.
const maxReadRows = 5000

// confidenceRankExpr is the warehouse's spelling of confidenceRank — GENERATED
// from it, so the severity order lives in exactly one place. Hand-writing the
// multiIf would be a second copy of the vocabulary, free to drift from the Go one
// the moment either changed, and the drift would be invisible: the query would
// still run and just rank wrongly.
//
// The interpolated values are this package's own constants, never caller input
// (TestConfidenceRankExprIsBuiltFromConstants holds that).
var confidenceRankExpr = buildConfidenceRankExpr()

func buildConfidenceRankExpr() string {
	var b strings.Builder
	b.WriteString("multiIf(")
	for _, c := range []string{ConfidenceExact, ConfidenceEstimated, ConfidencePercentOnly} {
		fmt.Fprintf(&b, "confidence = '%s', %d, ", c, confidenceRank(c))
	}
	fmt.Fprintf(&b, "%d)", confidenceRank(ConfidenceUnknown))
	return b.String()
}

// seriesQuery builds the per-provider dash read: every window instance of one
// account (or of every account on one provider) in [from, to), deduped, newest
// first.
//
// It is a PURE builder returning (sql, args) so the isolation contract is unit
// testable without a warehouse: org and subject ALWAYS lead as bound args, and no
// caller value is ever concatenated. `window` is allowlist-checked by the caller
// before it reaches here.
func seriesQuery(org, subject, provider, account, window string, from, to time.Time) (string, []any) {
	where := []string{"org = ?", "subject = ?", "provider = ?", "window_start >= ?", "window_start < ?"}
	args := []any{org, subject, provider, from.UTC(), to.UTC()}
	if account != "" {
		where = append(where, "account = ?")
		args = append(args, account)
	}
	if window != "" {
		where = append(where, "`window` = ?")
		args = append(args, window)
	}
	q := dedupedSelect + strings.Join(where, " AND ") + dedupedGroup +
		"\nORDER BY window_start DESC, lane ASC\nLIMIT ?"
	return q, append(args, maxReadRows)
}

// summaryQuery builds the global view's ACCOUNT side: the caller's own linked
// accounts, totalled per (provider, window).
//
// It sums the DEDUPED instances — the inner query collapses each window's polls to
// one row, and only then does the outer sum add them up. Summing the raw table
// would multiply every total by the poll rate.
//
// It groups by (provider, window) and NEVER across windows, because window classes
// NEST: a 6h lane's consumption is also inside its week lane's. Adding them would
// count the same tokens twice. Instances WITHIN one class are disjoint in time, so
// summing those is sound.
func summaryQuery(org, subject string, from, to time.Time) (string, []any) {
	inner := dedupedSelect +
		strings.Join([]string{"org = ?", "subject = ?", "window_start >= ?", "window_start < ?"}, " AND ") +
		dedupedGroup
	// max() over the confidence RANK takes the WEAKEST contributing lane — the only
	// honest aggregate. A sum whose parts were partly unknown cannot be called
	// exact: it is missing whatever those lanes did not report. (Ranking, not
	// max() over the strings: see confidenceRank.)
	q := `SELECT
  provider,
  window,
  sum(requests)          AS requests,
  sum(total_tokens)      AS total_tokens,
  sum(cost_cents)        AS cost_cents,
  max(used_pct)          AS used_pct,
  max(` + confidenceRankExpr + `) AS confidence_rank,
  count()                AS windows
FROM (` + inner + `)
GROUP BY provider, window
ORDER BY total_tokens DESC, provider ASC
LIMIT ?`
	return q, []any{org, subject, from.UTC(), to.UTC(), maxReadRows}
}

// hanzoQuery builds the global view's HANZO side: the org's Hanzo-routed usage per
// provider over the same window, from hanzo.cloud_usage — the same ledger (llmTable)
// and the same `provider` grouping the summary/analytics faces read, so the two can
// never disagree about what Hanzo charged.
//
// It is ORG-scoped, not user-scoped, and every row it produces says so (ScopeOrg).
// cloud_usage identifies a user as the qualified `<org>/<name>` form, which is not
// the bare subject this plane keys on; narrowing on an unproven identity match
// would silently report "no Hanzo usage" — a fabricated zero — rather than the
// truth. The scope label is the honest alternative until that equality is proven.
func hanzoQuery(org string, from, to time.Time) (string, []any) {
	q := `SELECT provider, count() AS requests, sum(total_tokens) AS total_tokens, ` +
		`sum(cost_cents) AS cost_cents FROM ` + llmTable + ` ` +
		`WHERE organization = ? AND timestamp >= ? AND timestamp < ? ` +
		`GROUP BY provider ORDER BY total_tokens DESC LIMIT ?`
	return q, []any{org, tsLiteral(from), tsLiteral(to), maxReadRows}
}

// Series reads one provider account's window instances. It returns (rows, true) when
// the warehouse answered and (nil, false) when it is unavailable, so the caller can
// say "unavailable" instead of showing zeros that read as "you used nothing".
func (w *warehouse) Series(ctx context.Context, org, subject, provider, account, window string, from, to time.Time) ([]Sample, bool) {
	if org == "" || subject == "" || provider == "" {
		return nil, false
	}
	if err := w.ensureAccountUsage(ctx); err != nil {
		return nil, false
	}
	q, args := seriesQuery(org, subject, provider, account, window, from, to)
	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, false
	}
	out := make([]Sample, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleOf(r))
	}
	return out, true
}

// sampleOf decodes one warehouse row into a Sample. Pure, so the whole read path's
// shape is testable without a warehouse.
func sampleOf(r map[string]any) Sample {
	return Sample{
		Provider:          dsString(r["provider"]),
		Account:           dsString(r["account"]),
		Plan:              dsString(r["plan"]),
		Kind:              dsString(r["kind"]),
		Machine:           dsString(r["machine"]),
		Lane:              dsString(r["lane"]),
		Window:            dsString(r["window"]),
		WindowMinutes:     int32(dsInt64(r["window_minutes"])),
		WindowStart:       dsTimeOf(r["window_start"]),
		ResetsAt:          dsTimeOf(r["resets_at"]),
		UsedPct:           dsFloat(r["used_pct"]),
		Confidence:        dsString(r["confidence"]),
		Synthetic:         dsInt64(r["synthetic"]) != 0,
		Requests:          dsInt64(r["requests"]),
		InputTokens:       dsInt64(r["input_tokens"]),
		OutputTokens:      dsInt64(r["output_tokens"]),
		TotalTokens:       dsInt64(r["total_tokens"]),
		CachedInputTokens: dsInt64(r["cached_input_tokens"]),
		CostCents:         dsInt64(r["cost_cents"]),
		CostLimitCents:    dsInt64(r["cost_limit_cents"]),
		Currency:          dsString(r["currency"]),
	}
}

// Total is one provider's usage in the global view, from ONE source at ONE scope.
type Total struct {
	Source     string
	Scope      string
	Provider   string
	Window     string
	Requests   int64
	Tokens     int64
	CostCents  int64
	UsedPct    float64
	Confidence string
	Windows    int64
}

// AccountTotals reads the caller's own account usage per (provider, window).
func (w *warehouse) AccountTotals(ctx context.Context, org, subject string, from, to time.Time) ([]Total, bool) {
	if org == "" || subject == "" {
		return nil, false
	}
	if err := w.ensureAccountUsage(ctx); err != nil {
		return nil, false
	}
	q, args := summaryQuery(org, subject, from, to)
	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, false
	}
	out := make([]Total, 0, len(rows))
	for _, r := range rows {
		out = append(out, accountTotalOf(r))
	}
	return out, true
}

// accountTotalOf decodes one account-side summary row. It STAMPS source+scope
// server-side (they are facts about which ledger answered, never data from it), so
// an account row can never present itself as Hanzo's cost of record.
func accountTotalOf(r map[string]any) Total {
	return Total{
		Source: SourceAccount, Scope: ScopeUser,
		Provider:  dsString(r["provider"]),
		Window:    dsString(r["window"]),
		Requests:  dsInt64(r["requests"]),
		Tokens:    dsInt64(r["total_tokens"]),
		CostCents: dsInt64(r["cost_cents"]),
		UsedPct:   dsFloat(r["used_pct"]),
		// The WEAKEST contributing lane's confidence — decoded from the rank the
		// query aggregated on, so a partly-unknown sum is never labelled exact.
		Confidence: confidenceOfRank(dsInt64(r["confidence_rank"])),
		Windows:    dsInt64(r["windows"]),
	}
}

// HanzoTotals reads the org's Hanzo-routed usage per provider over the same window.
// Its rows are always `exact` — cloud_usage counts real calls we billed.
func (w *warehouse) HanzoTotals(ctx context.Context, org string, from, to time.Time) ([]Total, bool) {
	if org == "" {
		return nil, false
	}
	if !aiobject.DatastoreEnabled() {
		return nil, false
	}
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return nil, false
	}
	q, args := hanzoQuery(org, from, to)
	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, false
	}
	out := make([]Total, 0, len(rows))
	for _, r := range rows {
		out = append(out, hanzoTotalOf(r))
	}
	return out, true
}

// hanzoTotalOf decodes one Hanzo-side summary row. Source+scope are stamped
// server-side, and confidence is `exact` because cloud_usage counts calls we
// actually billed — it never estimates. It carries NO window: cloud_usage is a
// per-call ledger, not a window meter, so its totals are simply the requested
// range. Fabricating a window class here would invent a quota that does not exist.
func hanzoTotalOf(r map[string]any) Total {
	return Total{
		Source: SourceHanzo, Scope: ScopeOrg,
		Provider:   dsString(r["provider"]),
		Requests:   dsInt64(r["requests"]),
		Tokens:     dsInt64(r["total_tokens"]),
		CostCents:  dsInt64(r["cost_cents"]),
		Confidence: ConfidenceExact,
	}
}

// ── cell coercion ────────────────────────────────────────────────────────────
// DatastoreQuery decodes each column into its native scan type; these accept the
// widths the driver can hand back, so a column's exact type never reaches a caller.

func dsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func dsInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case uint64:
		if n > 1<<62 {
			return 1 << 62
		}
		return int64(n)
	case uint32:
		return int64(n)
	case int32:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func dsFloat(v any) float64 {
	switch f := v.(type) {
	case float64:
		return finite(f)
	case float32:
		return finite(float64(f))
	case uint64:
		return float64(f)
	case int64:
		return float64(f)
	}
	return 0
}

// epoch is the sentinel for an ABSENT instant.
//
// Go's zero time is year 1, which a DateTime64 column cannot represent — binding it
// would break or silently corrupt the row, and an absent resets_at is the COMMON
// case (most meters report no boundary). So absence is stored as the epoch, which
// the column represents natively and reads back unambiguously as "not set": no real
// window resets in 1970. dsInstant applies it on write, dsTimeOf reverses it on
// read, and the wire omits the field entirely — absence never becomes a date.
var epoch = time.Unix(0, 0).UTC()

// dsInstant renders an instant for a DateTime64 column, mapping the absent (zero)
// instant to the epoch sentinel.
func dsInstant(t time.Time) time.Time {
	if t.IsZero() {
		return epoch
	}
	return t.UTC()
}

// dsTimeOf decodes a DateTime64 cell, mapping the epoch sentinel back to absent.
func dsTimeOf(v any) time.Time {
	t, ok := v.(time.Time)
	if !ok || t.Unix() <= 0 {
		return time.Time{}
	}
	return t.UTC()
}
