package o11y

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
)

// The scoped logs read. Application/infra logs live in the SigNoz ClickHouse
// (`signoz_logs.distributed_logs_v2`) on the SAME datastore ClickHouse server the
// shared ai/object client already owns — so this reuses aiobject.DatastoreQuery
// (ONE datastore client, ONE KMS-injected cred namespace, per the CTO
// consolidation) rather than opening a second connection. It queries the logs DB
// cross-database by fully-qualified name; the datastore user has the read grant
// (the same creds that VERIFIED the live query).
//
// TENANT ISOLATION (the whole point):
//   - Infra logs carry NO org attribute (they are collected from pod stdout,
//     tagged only with `resources_string['app']`). So the org predicate can only
//     ever match rows that DO carry an org attribution
//     (`attributes_string['org']`, emitted going forward by cloud's request
//     logger). For a NON-admin org the WHERE pins `attributes_string['org'] =
//     {org}` — which can match ONLY that org's own rows (empty for pre-emit
//     history: fail-closed, NEVER another tenant's stream).
//   - ONLY the admin org (validated global admin) may read the full,
//     unattributed shared stream (no org predicate) — the intended infra god-view.
//   - Every value (app, org, since, cursor, limit) is a BOUND positional
//     parameter — never string-interpolated — so a crafted product/org cannot
//     inject ClickHouse SQL. The product/app is additionally allowlisted
//     (resolveService) before it ever reaches here.

const (
	// logsTableEnv overrides the fully-qualified logs table (deployment-varying).
	logsTableEnv     = "O11Y_LOGS_TABLE"
	defaultLogsTable = "signoz_logs.distributed_logs_v2"

	// logsOrgAttrEnv overrides the ClickHouse expression that carries a row's org
	// attribution. Default is the per-record string-attribute map key `org`, where
	// cloud's request logger lands the validated tenant going forward.
	logsOrgAttrEnv     = "O11Y_LOGS_ORG_ATTR"
	defaultLogsOrgAttr = "attributes_string['org']"

	// Log read bounds. A read is always time-boxed and row-capped so it can never
	// become an OLAP-scan DoS.
	defaultLogSinceHours = 1
	maxLogSinceHours     = 24 * 7 // one week ceiling on the lookback
	defaultLogLimit      = 200
	maxLogLimit          = 1000
)

// logLine is one returned log row.
type logLine struct {
	// TS is the RFC3339 UTC timestamp (human/display).
	TS string `json:"ts"`
	// TSNano is the raw UInt64 nanosecond epoch as a string (JS-safe cursor).
	TSNano   string `json:"tsNano"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
	App      string `json:"app"`
}

// logQuery is the validated, server-resolved shape of a logs read. Everything in
// it is server-derived (org, admin from the validated principal; app from the
// allowlist); the client only chooses the product, since, limit, and a cursor.
type logQuery struct {
	app        string // allowlisted service app label
	org        string // validated tenant (pinned unless admin)
	admin      bool   // validated global admin → unattributed god-view
	sinceHours int
	afterNano  uint64 // tail cursor: only rows strictly newer than this (0 = none)
	limit      int
}

// buildLogsSQL renders the parameterized SELECT and its positional args. It is a
// PURE function (no I/O) so the org-predicate injection is unit-tested directly:
// a non-admin query ALWAYS carries the org predicate; an admin query NEVER
// pins org (full stream). No value is interpolated — every `?` is bound.
func buildLogsSQL(q logQuery) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT resources_string['app'] AS app, timestamp, severity_text, body FROM ")
	b.WriteString(logsTable())
	// Lookback: rows within the last N hours (bound N).
	b.WriteString(" WHERE timestamp > toUnixTimestamp64Nano(now64() - toIntervalHour(?))")
	args := []any{uint64(q.sinceHours)}
	// App (allowlisted) bound as a parameter.
	b.WriteString(" AND resources_string['app'] = ?")
	args = append(args, q.app)
	// THE tenant gate. A non-admin is pinned to rows carrying its own org
	// attribution; an admin sees the full unattributed stream.
	if !q.admin {
		b.WriteString(" AND ")
		b.WriteString(logsOrgAttr())
		b.WriteString(" = ?")
		args = append(args, q.org)
	}
	// Tail cursor: only rows strictly newer than the client's last-seen ts.
	if q.afterNano > 0 {
		b.WriteString(" AND timestamp > ?")
		args = append(args, q.afterNano)
	}
	b.WriteString(" ORDER BY timestamp DESC LIMIT ?")
	args = append(args, uint64(q.limit))
	return b.String(), args
}

// queryLogs runs the scoped logs read over the shared datastore client and
// returns the rows newest-first plus the max nanosecond cursor (for tailing).
// An unwired datastore is an honest empty result (not a fabricated line).
func queryLogs(ctx context.Context, q logQuery) ([]logLine, string, error) {
	if !aiobject.DatastoreEnabled() {
		// Honest: the log warehouse is not connected on this deployment.
		return []logLine{}, "", nil
	}
	sql, args := buildLogsSQL(q)
	rows, err := aiobject.DatastoreQuery(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("o11y logs: query: %w", err)
	}
	out := make([]logLine, 0, len(rows))
	var maxNano uint64
	for _, r := range rows {
		nano := asUint64(r["timestamp"])
		if nano > maxNano {
			maxNano = nano
		}
		out = append(out, logLine{
			TS:       nanoToRFC3339(nano),
			TSNano:   strconv.FormatUint(nano, 10),
			Severity: asString(r["severity_text"]),
			Body:     asString(r["body"]),
			App:      asString(r["app"]),
		})
	}
	cursor := ""
	if maxNano > 0 {
		cursor = strconv.FormatUint(maxNano, 10)
	}
	return out, cursor, nil
}

func logsTable() string {
	if v := strings.TrimSpace(os.Getenv(logsTableEnv)); v != "" {
		return v
	}
	return defaultLogsTable
}

func logsOrgAttr() string {
	if v := strings.TrimSpace(os.Getenv(logsOrgAttrEnv)); v != "" {
		return v
	}
	return defaultLogsOrgAttr
}

// boundSinceHours clamps the client `since` (hours) to [1, maxLogSinceHours].
func boundSinceHours(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return defaultLogSinceHours
	}
	if n > maxLogSinceHours {
		return maxLogSinceHours
	}
	return n
}

// boundLogLimit clamps the client `limit` to [1, maxLogLimit].
func boundLogLimit(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return defaultLogLimit
	}
	if n > maxLogLimit {
		return maxLogLimit
	}
	return n
}

// parseCursor parses the `after` tail cursor (a UInt64 nanosecond epoch string).
// A malformed cursor is ignored (0) rather than erroring — a fresh tail restarts.
func parseCursor(raw string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// nanoToRFC3339 renders a UInt64 nanosecond epoch as RFC3339 UTC (empty for 0).
func nanoToRFC3339(nano uint64) string {
	if nano == 0 {
		return ""
	}
	return time.Unix(0, int64(nano)).UTC().Format(time.RFC3339)
}
