package o11y

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
)

// The scoped logs read serves GET /v1/o11y/logs — a live, org-scoped log stream for
// a product. Application/infra logs live in the o11y datastore on the SAME
// datastore datastore server the shared ai/object client already owns, so this
// reuses datastore.Query (ONE datastore client, ONE KMS-injected cred
// namespace) rather than opening a second connection.
//
// TWO honest tenant views over ONE store:
//
//   - ADMIN (validated SuperAdmin, c.IsAdmin()): the product's raw infra log stream
//     from o11y_logs.distributed_logs_v2, filtered resources_string['app']=<workload>.
//     These stdout lines carry NO org label, so ONLY the platform operator may see
//     them — the Hanzo-staff infra view.
//   - EVERY OTHER org: its OWN request log stream, derived from org-tagged spans in
//     o11y_traces (attributes_string['hanzo.org']=<org>), scoped to the product's
//     routes/service. A tenant can NEVER see another tenant's rows (org bound as a
//     positional parameter, never interpolated) and can NEVER see the unattributed
//     infra stream (that path is gated on admin).
//
// Every value (app, org, since, cursor, limit) is a BOUND positional parameter —
// never string-interpolated — so a crafted product/org cannot inject datastore
// SQL; the product/app is additionally allowlisted (resolveService) before it ever
// reaches here. Live tail: the client polls with ?sinceNs=<cursor> (the nextCursor
// from the prior response); absent it, the last ?window seconds are returned. Every
// query is LIMIT-bounded (OLAP-scan safety) and time-boxed.

const (
	defaultLogWindowSec = 900   // 15m initial window when no cursor is supplied
	maxLogWindowSec     = 86400 // 24h ceiling
	logReadTimeout      = 8 * time.Second

	defaultLogLimit = 200
	maxLogLimit     = 1000
)

// logLine is one returned log row.
type logLine struct {
	TS       string `json:"ts"`       // RFC3339 (UTC)
	TSNano   int64  `json:"tsNano"`   // nanosecond cursor
	Severity string `json:"severity"` // INFO | WARN | ERROR | ...
	Body     string `json:"body"`
	Source   string `json:"source"` // "infra" (stdout) | "request" (org request log)
}

// logsResponse is the scoped logs response.
type logsResponse struct {
	Product    string    `json:"product"`
	View       string    `json:"view"` // "infra" (admin) | "request" (per-org)
	Lines      []logLine `json:"lines"`
	NextCursor int64     `json:"nextCursor"` // pass back as ?sinceNs for the next tail poll
}

// queryLogs runs the correct view for the caller and returns the rows newest-first
// plus the max nanosecond cursor. An unwired datastore is an honest empty result
// (not a fabricated line).
func queryLogs(ctx context.Context, svc service, org string, admin bool, sinceNs int64, windowSec, limit int) (logsResponse, error) {
	resp := logsResponse{Product: svc.ID, View: viewFor(admin), Lines: []logLine{}}
	if !datastore.Ready() {
		// Honest: the log warehouse is not connected on this deployment.
		return resp, nil
	}
	var (
		lines []logLine
		err   error
	)
	if admin {
		lines, err = infraLogs(ctx, svc.App, sinceNs, windowSec, limit)
	} else {
		lines, err = requestLogs(ctx, org, svc, sinceNs, windowSec, limit)
	}
	if err != nil {
		return logsResponse{}, err
	}
	next := sinceNs
	for _, ln := range lines {
		if ln.TSNano > next {
			next = ln.TSNano
		}
	}
	resp.Lines = lines
	resp.NextCursor = next
	return resp, nil
}

func viewFor(admin bool) string {
	if admin {
		return "infra"
	}
	return "request"
}

// infraLogs reads the product's raw stdout stream from o11y_logs. Admin-only:
// these lines are unattributed to any tenant, so the caller has already been gated
// on admin. app is bound as a positional parameter.
func infraLogs(ctx context.Context, app string, sinceNs int64, windowSec, limit int) ([]logLine, error) {
	q := "SELECT timestamp, severity_text, body FROM o11y_logs.distributed_logs_v2 WHERE resources_string['app'] = ?"
	args := []any{app}
	if sinceNs > 0 {
		q += " AND timestamp > ?"
		args = append(args, uint64(sinceNs))
	} else {
		q += " AND timestamp > toUnixTimestamp64Nano(now64() - toIntervalSecond(?))"
		args = append(args, windowSec)
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, uint64(limit))

	rows, err := datastore.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query infra logs: %w", err)
	}
	out := make([]logLine, 0, len(rows))
	for _, r := range rows {
		ns := asInt64(r["timestamp"])
		out = append(out, logLine{
			TS:       nsToRFC3339(ns),
			TSNano:   ns,
			Severity: normalizeSeverity(asString(r["severity_text"])),
			Body:     asString(r["body"]),
			Source:   "infra",
		})
	}
	return out, nil
}

// requestLogs derives a per-org request log from org-tagged spans in o11y_traces.
// org is bound as a positional parameter (never interpolated) and is the FIRST,
// mandatory predicate — a tenant sees only its own requests. The product scope is a
// route prefix (/v1/<product>/…) OR the product's own serviceName (separately
// deployed products), so it works for both cloud-fused and standalone products.
func requestLogs(ctx context.Context, org string, svc service, sinceNs int64, windowSec, limit int) ([]logLine, error) {
	routePrefix := "/v1/" + svc.ID
	// response_status_code is LowCardinality(String) in o11y — coerce to Int in SQL
	// so the row read gets a number (asInt64 on a string yields 0).
	q := "SELECT timestamp, name, httpRoute, toInt32OrZero(response_status_code) AS http_status, status_code, duration_nano " +
		"FROM o11y_traces.distributed_o11y_index_v3 WHERE attributes_string['hanzo.org'] = ? " +
		"AND (httpRoute = ? OR startsWith(httpRoute, ?) OR serviceName = ?)"
	args := []any{org, routePrefix, routePrefix + "/", svc.App}
	if sinceNs > 0 {
		q += " AND toUnixTimestamp64Nano(timestamp) > ?"
		args = append(args, uint64(sinceNs))
	} else {
		q += " AND timestamp > now64() - toIntervalSecond(?)"
		args = append(args, windowSec)
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, uint64(limit))

	rows, err := datastore.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query request logs: %w", err)
	}
	out := make([]logLine, 0, len(rows))
	for _, r := range rows {
		ts := asTime(r["timestamp"])
		httpStatus := asInt64(r["http_status"])
		spanStatus := asInt64(r["status_code"])
		durMs := float64(asInt64(r["duration_nano"])) / 1e6
		route := asString(r["httpRoute"])
		if route == "" {
			route = asString(r["name"])
		}
		out = append(out, logLine{
			TS:       ts.UTC().Format(time.RFC3339),
			TSNano:   ts.UnixNano(),
			Severity: severityForStatus(httpStatus, spanStatus),
			Body:     fmt.Sprintf("%s status=%d dur=%.1fms", route, httpStatus, durMs),
			Source:   "request",
		})
	}
	return out, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// boundSinceNs parses the `sinceNs` tail cursor (nanosecond epoch). A malformed or
// negative value is ignored (0) — a fresh tail restarts.
func boundSinceNs(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// boundWindowSec clamps the client `window` (seconds) to [1, maxLogWindowSec].
func boundWindowSec(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return defaultLogWindowSec
	}
	if n > maxLogWindowSec {
		return maxLogWindowSec
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

// nsToRFC3339 renders a nanosecond epoch as RFC3339 UTC (empty for ≤0).
func nsToRFC3339(ns int64) string {
	if ns <= 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339)
}

// normalizeSeverity uppercases an o11y severity_text, defaulting empty to INFO.
func normalizeSeverity(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "INFO"
	}
	return s
}

// severityForStatus maps an HTTP/span status onto a log severity for the request view.
func severityForStatus(httpStatus, spanStatus int64) string {
	if httpStatus >= 500 || spanStatus == 2 {
		return "ERROR"
	}
	if httpStatus >= 400 {
		return "WARN"
	}
	return "INFO"
}
