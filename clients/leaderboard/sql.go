// Pure, I/O-free core of the usage leaderboard + activity reads: the metric
// allowlist, the period→day-window resolver, the day literal, and the datastore
// SQL BUILDERS. Everything here is a pure function so the tests drive it with
// plain values — no datastore, no HTTP.
//
// INJECTION SAFETY (the bar). The tenant key (organization) and every user-
// supplied value (subject id, day bounds, self-rank threshold) is ALWAYS a bound
// `?` parameter, appended to the args slice — NEVER string-interpolated into the
// SQL. The only tokens interpolated are (a) the metric column, taken from the
// closed `metricColumn` allowlist (a caller's `metric=` can only ever select one
// of three fixed column names, or be rejected), and (b) the LIMIT, a server-
// clamped int. This mirrors the proven house pattern (ai/object cloud_usage.go
// whereClause, clients/analytics query.go llmWhere): org bound positionally, the
// bucket/limit a closed enum / validated int. The builders return (sql, args) so
// a test can assert a hostile org slug or metric lands in args (or is rejected),
// never in the SQL string.
package leaderboard

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// rollupTable is the ONE derived per-day usage rollup the leaderboard + activity
// reads scan (SummingMergeTree, kept fresh by rollupMV, seeded by the deploy-gated
// backfill). It is a DERIVED lens over hanzo.cloud_usage — never a second metering
// path. See rollup.go for its DDL.
const rollupTable = "hanzo.usage_rollup_daily"

// metricColumn is the CLOSED allowlist mapping a caller's `metric=` to the exact
// rollup column ranked/ordered on. A value outside this map is REJECTED (never
// interpolated). This is the only user-influenced token that reaches the SQL text,
// and it can only ever be one of these three fixed identifiers.
var metricColumn = map[string]string{
	"tokens":   "total_tokens",
	"requests": "requests",
	"cost":     "cost_cents",
}

// resolveMetric maps a caller's metric label to its rollup column. Empty → the
// tokens default. An unknown label → ("", false) so the handler answers 400
// (fail closed) rather than guessing.
func resolveMetric(metric string) (column string, ok bool) {
	m := strings.ToLower(strings.TrimSpace(metric))
	if m == "" {
		return "total_tokens", true
	}
	col, ok := metricColumn[m]
	return col, ok
}

// maxLeaderboardRows bounds a leaderboard page. The rollup is small (org × user ×
// model × day), but a leaderboard is a top-N view — an unbounded LIMIT is never
// useful and a huge one is a cheap DoS, so the handler clamps to this.
const maxLeaderboardRows = 100

// clampLimit bounds a requested row count to [1, maxLeaderboardRows], defaulting
// to def. The result is a validated int, safe to interpolate as LIMIT.
func clampLimit(requested, def int) int {
	if requested <= 0 {
		return def
	}
	if requested > maxLeaderboardRows {
		return maxLeaderboardRows
	}
	return requested
}

// window is a resolved [From, To) day range in UTC. HasFrom is false only for the
// "all" period (no lower bound); To is always the exclusive end (start of tomorrow
// UTC) so the current day is included.
type window struct {
	From    time.Time
	To      time.Time
	HasFrom bool
	Label   string
}

// resolvePeriod maps a leaderboard period label to a [from, to) day window. Pure:
// `now` is injected so tests are deterministic. Default (empty) is "month" — the
// most useful leaderboard default. An unknown label is rejected (fail closed).
func resolvePeriod(period string, now time.Time) (window, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	to := today.Add(24 * time.Hour) // exclusive end = start of tomorrow → today included
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "", "month", "30d":
		return window{From: today.Add(-29 * 24 * time.Hour), To: to, HasFrom: true, Label: "month"}, nil
	case "day", "today", "24h", "1d":
		return window{From: today, To: to, HasFrom: true, Label: "day"}, nil
	case "week", "7d":
		return window{From: today.Add(-6 * 24 * time.Hour), To: to, HasFrom: true, Label: "week"}, nil
	case "all":
		return window{To: to, HasFrom: false, Label: "all"}, nil
	default:
		return window{}, fmt.Errorf("unknown period %q (use day|week|month|all)", period)
	}
}

// resolveRange maps explicit from/to day strings (activity's custom range) to a
// [from, to) window. Both are "2006-01-02" (or RFC3339, truncated to the day). An
// empty from defaults to 90 days back; an empty to defaults to tomorrow. The
// window is clamped to a hard 366-day span so a single activity read can never
// scan an unbounded range.
func resolveRange(fromStr, toStr string, now time.Time) (window, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	to := today.Add(24 * time.Hour)
	if s := strings.TrimSpace(toStr); s != "" {
		t, err := parseDay(s)
		if err != nil {
			return window{}, fmt.Errorf("invalid to: %w", err)
		}
		to = t.Add(24 * time.Hour) // make the given day inclusive
	}
	from := to.Add(-90 * 24 * time.Hour)
	if s := strings.TrimSpace(fromStr); s != "" {
		f, err := parseDay(s)
		if err != nil {
			return window{}, fmt.Errorf("invalid from: %w", err)
		}
		from = f
	}
	if !to.After(from) {
		return window{}, fmt.Errorf("to must be after from")
	}
	if to.Sub(from) > 366*24*time.Hour {
		from = to.Add(-366 * 24 * time.Hour) // clamp the span
	}
	return window{From: from, To: to, HasFrom: true, Label: "custom"}, nil
}

// parseDay accepts "2006-01-02", RFC3339, or unix seconds and returns the UTC
// start of that day.
func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Truncate(24 * time.Hour), nil
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil && secs > 0 {
		return time.Unix(secs, 0).UTC().Truncate(24 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("not a date (2006-01-02), RFC3339, or unix seconds: %q", s)
}

// dayLiteral formats a time as a datastore Date literal (UTC, no clock). Bound as a
// string arg — the datastore driver accepts 'YYYY-MM-DD' for a Date comparison,
// identical in spirit to cloud_usage.go's DateTime tsLiteral.
func dayLiteral(t time.Time) string { return t.UTC().Format("2006-01-02") }

// dayBounds appends the window's day predicate(s) to a WHERE builder. The lower
// bound is omitted for "all" (HasFrom=false); the upper bound is always present.
// Every bound is a `?` param appended to args — never interpolated.
func dayBounds(w window, where []string, args []any) ([]string, []any) {
	if w.HasFrom {
		where = append(where, "day >= ?")
		args = append(args, dayLiteral(w.From))
	}
	where = append(where, "day < ?")
	args = append(args, dayLiteral(w.To))
	return where, args
}

// ── Builders ────────────────────────────────────────────────────────────────
//
// Every builder puts `organization = ?` FIRST for the org-scoped reads (the
// tenant gate and the leading ORDER BY column of the rollup, so it is also the
// index-efficient predicate). metricCol is pre-resolved from the allowlist;
// limit is pre-clamped.

// buildUserBoardSQL ranks the users of ONE org over the window by metricCol. The
// org is the leading bound predicate — a caller can never read another org's rows.
func buildUserBoardSQL(org string, w window, metricCol string, limit int) (string, []any) {
	where := []string{"organization = ?"}
	args := []any{org}
	where, args = dayBounds(w, where, args)
	sql := "SELECT user_id, sum(requests) AS requests, sum(total_tokens) AS total_tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + rollupTable +
		" WHERE " + strings.Join(where, " AND ") +
		" GROUP BY user_id ORDER BY " + metricCol + " DESC, requests DESC LIMIT " + strconv.Itoa(limit)
	return sql, args
}

// buildOrgBoardSQL ranks organizations over the window by metricCol. `orgs` is the
// visibility restriction: nil = no restriction (SuperAdmin sees every org); a
// non-empty slice restricts to those opted-in orgs (each bound `?`); an EMPTY
// slice means "no orgs are visible" and the caller short-circuits to an empty
// board WITHOUT running this (guarded by the caller). Org-level aggregates only —
// no user identity ever appears in an org board.
func buildOrgBoardSQL(w window, metricCol string, limit int, orgs []string) (string, []any) {
	var where []string
	var args []any
	where, args = dayBounds(w, where, args)
	if orgs != nil {
		ph := make([]string, len(orgs))
		for i, o := range orgs {
			ph[i] = "?"
			args = append(args, o)
		}
		where = append(where, "organization IN ("+strings.Join(ph, ",")+")")
	}
	sql := "SELECT organization, sum(requests) AS requests, sum(total_tokens) AS total_tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + rollupTable +
		" WHERE " + strings.Join(where, " AND ") +
		" GROUP BY organization ORDER BY " + metricCol + " DESC, requests DESC LIMIT " + strconv.Itoa(limit)
	return sql, args
}

// buildSelfAggSQL is the caller's OWN windowed aggregate within its org — the
// numerator of "your rank". org + user_id are both leading bound predicates.
func buildSelfAggSQL(org, userID string, w window) (string, []any) {
	where := []string{"organization = ?", "user_id = ?"}
	args := []any{org, userID}
	where, args = dayBounds(w, where, args)
	sql := "SELECT sum(requests) AS requests, sum(total_tokens) AS total_tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + rollupTable +
		" WHERE " + strings.Join(where, " AND ")
	return sql, args
}

// buildAboveCountSQL counts the users in `org` whose windowed metric STRICTLY
// exceeds `threshold` — so the caller's rank is that count + 1. metricCol is
// allowlisted; org, the day bounds, and threshold are all bound params.
func buildAboveCountSQL(org string, w window, metricCol string, threshold int64) (string, []any) {
	where := []string{"organization = ?"}
	args := []any{org}
	where, args = dayBounds(w, where, args)
	inner := "SELECT user_id FROM " + rollupTable + " WHERE " + strings.Join(where, " AND ") +
		" GROUP BY user_id HAVING sum(" + metricCol + ") > ?"
	args = append(args, threshold)
	return "SELECT count() AS above FROM (" + inner + ")", args
}

// buildUserCountSQL counts the distinct users of `org` active in the window — the
// denominator "of N" for a rank.
func buildUserCountSQL(org string, w window) (string, []any) {
	where := []string{"organization = ?"}
	args := []any{org}
	where, args = dayBounds(w, where, args)
	return "SELECT uniqExact(user_id) AS users FROM " + rollupTable + " WHERE " + strings.Join(where, " AND "), args
}

// buildOrgCountSQL counts the distinct orgs active in the window (denominator for a
// global org board). `orgs` restricts identically to buildOrgBoardSQL.
func buildOrgCountSQL(w window, orgs []string) (string, []any) {
	var where []string
	var args []any
	where, args = dayBounds(w, where, args)
	if orgs != nil {
		ph := make([]string, len(orgs))
		for i, o := range orgs {
			ph[i] = "?"
			args = append(args, o)
		}
		where = append(where, "organization IN ("+strings.Join(ph, ",")+")")
	}
	return "SELECT uniqExact(organization) AS orgs FROM " + rollupTable + " WHERE " + strings.Join(where, " AND "), args
}

// buildOrgAggSQL is ONE org's windowed aggregate — the numerator of the caller's
// own global org rank. org is the leading bound predicate.
func buildOrgAggSQL(org string, w window) (string, []any) {
	where := []string{"organization = ?"}
	args := []any{org}
	where, args = dayBounds(w, where, args)
	return "SELECT sum(requests) AS requests, sum(total_tokens) AS total_tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + rollupTable + " WHERE " + strings.Join(where, " AND "), args
}

// buildOrgAboveCountSQL counts the orgs whose windowed metric STRICTLY exceeds
// threshold — a bare count (no identity) yielding the caller's own org rank. `orgs`
// restricts the universe identically to buildOrgBoardSQL: nil = all orgs (a platform
// admin's global rank); a non-empty set = rank only within the opted-in public board
// (so a regular caller never learns the platform-wide org universe). metricCol is
// allowlisted; the day bounds, each org, and threshold are bound params.
func buildOrgAboveCountSQL(w window, metricCol string, threshold int64, orgs []string) (string, []any) {
	var where []string
	var args []any
	where, args = dayBounds(w, where, args)
	if orgs != nil {
		ph := make([]string, len(orgs))
		for i, o := range orgs {
			ph[i] = "?"
			args = append(args, o)
		}
		where = append(where, "organization IN ("+strings.Join(ph, ",")+")")
	}
	inner := "SELECT organization FROM " + rollupTable + " WHERE " + strings.Join(where, " AND ") +
		" GROUP BY organization HAVING sum(" + metricCol + ") > ?"
	args = append(args, threshold)
	return "SELECT count() AS above FROM (" + inner + ")", args
}

// buildActivitySQL is the per-day series for one subject over the window — the
// contribution-heatmap source. subjectUser="" ⇒ an org-wide series (org is the
// only subject predicate); non-empty ⇒ a single user's series (org + user_id, both
// bound). org is ALWAYS the leading bound predicate.
func buildActivitySQL(org, subjectUser string, w window) (string, []any) {
	where := []string{"organization = ?"}
	args := []any{org}
	if subjectUser != "" {
		where = append(where, "user_id = ?")
		args = append(args, subjectUser)
	}
	where, args = dayBounds(w, where, args)
	sql := "SELECT day, sum(requests) AS requests, sum(total_tokens) AS total_tokens, " +
		"sum(cost_cents) AS cost_cents FROM " + rollupTable +
		" WHERE " + strings.Join(where, " AND ") +
		" GROUP BY day ORDER BY day"
	return sql, args
}
