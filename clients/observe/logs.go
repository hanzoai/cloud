package observe

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// logs.go serves GET /v1/o11y/logs — a live, org-scoped log stream for a product.
//
// TWO honest tenant views over ONE store (ClickHouse via the shared ai/object
// datastore client — no new connection, KMS-injected creds):
//
//   - ADMIN org (IAM_ADMIN_ORG): the product's raw infra log stream from
//     signoz_logs.distributed_logs_v2, filtered resources_string['app']=<service>.
//     These stdout lines carry NO org label, so ONLY the platform operator may see
//     them — this is the Hanzo-staff view.
//   - EVERY OTHER org: its OWN request log stream, derived from org-tagged spans in
//     signoz_traces (attributes_string['hanzo.org']=<org>), scoped to the product's
//     routes/service. A tenant can NEVER see another tenant's rows (org bound as a
//     positional parameter, never interpolated) and can NEVER see the unattributed
//     infra stream (that path is gated on isAdmin).
//
// Live tail: the client polls with ?sinceNs=<cursor> (the nextCursor from the prior
// response); absent it, the last ?window seconds (default 900) are returned. Every
// query is LIMIT-bounded (OLAP-scan safety) and time-boxed.

const (
	defaultLogWindowSec = 900   // 15m initial window when no cursor is supplied
	maxLogWindowSec     = 86400 // 24h ceiling
	logReadTimeout      = 8 * time.Second
)

type logLine struct {
	TS       string `json:"ts"`       // RFC3339 (UTC)
	TSNano   int64  `json:"tsNano"`   // nanosecond cursor
	Severity string `json:"severity"` // INFO | WARN | ERROR | ...
	Body     string `json:"body"`
	Source   string `json:"source"` // "infra" (stdout) | "request" (org request log)
}

type logsResponse struct {
	Product    string    `json:"product"`
	View       string    `json:"view"` // "infra" (admin) | "request" (per-org)
	Lines      []logLine `json:"lines"`
	NextCursor int64     `json:"nextCursor"` // pass back as ?sinceNs for the next tail poll
}

func (s *service) getLogs(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProductQuery(c)
	if err != nil {
		return err
	}
	if !aiobject.DatastoreEnabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "o11y logs: datastore not connected")
	}
	service := serviceFor(product)
	limit := logLimit(c)
	sinceNs := parseSinceNs(c)
	windowSec := parseWindowSec(c)

	ctx, cancel := ctxWithTimeout(c.Context(), logReadTimeout)
	defer cancel()

	var (
		lines []logLine
		view  string
	)
	if s.isAdmin(org) {
		view = "infra"
		lines, err = s.infraLogs(ctx, service, sinceNs, windowSec, limit)
	} else {
		view = "request"
		lines, err = s.requestLogs(ctx, org, product, service, sinceNs, windowSec, limit)
	}
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "o11y logs: %v", err)
	}

	next := sinceNs
	for _, ln := range lines {
		if ln.TSNano > next {
			next = ln.TSNano
		}
	}
	return c.JSON(http.StatusOK, logsResponse{Product: product, View: view, Lines: lines, NextCursor: next})
}

// infraLogs reads the product's raw stdout stream from signoz_logs. Admin-only:
// these lines are unattributed to any tenant, so the caller has already been gated
// on isAdmin. app is bound as a positional parameter.
func (s *service) infraLogs(ctx context.Context, service string, sinceNs int64, windowSec, limit int) ([]logLine, error) {
	q := "SELECT timestamp, severity_text, body FROM signoz_logs.distributed_logs_v2 WHERE resources_string['app'] = ?"
	args := []any{service}
	if sinceNs > 0 {
		q += " AND timestamp > ?"
		args = append(args, uint64(sinceNs))
	} else {
		q += " AND timestamp > toUnixTimestamp64Nano(now64() - toIntervalSecond(?))"
		args = append(args, windowSec)
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, uint64(limit))

	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
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

// requestLogs derives a per-org request log from org-tagged spans in signoz_traces.
// org is bound as a positional parameter (never interpolated) and is the FIRST,
// mandatory predicate — a tenant sees only its own requests. The product scope is a
// route prefix (/v1/<product>/…) OR the product's own serviceName (separately
// deployed products), so it works for both cloud-fused and standalone products.
func (s *service) requestLogs(ctx context.Context, org, product, service string, sinceNs int64, windowSec, limit int) ([]logLine, error) {
	routePrefix := "/v1/" + product
	q := "SELECT timestamp, name, httpRoute, response_status_code, status_code, duration_nano " +
		"FROM signoz_traces.distributed_signoz_index_v3 WHERE attributes_string['hanzo.org'] = ? " +
		"AND (httpRoute = ? OR startsWith(httpRoute, ?) OR serviceName = ?)"
	args := []any{org, routePrefix, routePrefix + "/", service}
	if sinceNs > 0 {
		q += " AND toUnixTimestamp64Nano(timestamp) > ?"
		args = append(args, uint64(sinceNs))
	} else {
		q += " AND timestamp > now64() - toIntervalSecond(?)"
		args = append(args, windowSec)
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, uint64(limit))

	rows, err := aiobject.DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query request logs: %w", err)
	}
	out := make([]logLine, 0, len(rows))
	for _, r := range rows {
		ts := asTime(r["timestamp"])
		httpStatus := asInt64(r["response_status_code"])
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

func parseSinceNs(c *zip.Ctx) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(c.Query("sinceNs")), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseWindowSec(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("window")))
	if err != nil || n <= 0 {
		return defaultLogWindowSec
	}
	if n > maxLogWindowSec {
		return maxLogWindowSec
	}
	return n
}

func nsToRFC3339(ns int64) string {
	if ns <= 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339)
}

// normalizeSeverity upppercases a signoz severity_text, defaulting empty to INFO.
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
