package o11y

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// traces.go serves GET /v1/o11y/traces — the trace LIST for the caller's org,
// and the one address in this family nothing answered. hanzoai/o11y declares the
// trace DETAIL (GET /v1/o11y/traces/{traceId}, its SearchTraces — the spans of
// ONE trace), the field catalog, and three POST projections of a single trace;
// every one of them needs a trace id the caller does not have yet, and the bare
// collection that hands one out was a 404. So the detail read was reachable only
// by someone who already knew the answer.
//
// This claims the COLLECTION and nothing else. A `/traces/{id}` of our own would
// be a second declaration at an address the module already owns — the refusal to
// compose scope.go's header is written about — and the detail read is not
// missing, so there is nothing to add there.
//
// WHAT IT READS. event.trace, the summary the span writer (planesink.go) fills:
// one PARTIAL row per write batch per trace, folded by an AggregatingMergeTree
// over SimpleAggregateFunction(min/max/sum) columns. A merge is asynchronous and
// is never a promise, so this read AGGREGATES — min(start), max(`end`),
// sum(num_spans), GROUP BY trace_id. Skipping that would report a BATCH as a
// trace: one 400-span trace would come back as the eleven partials that carried
// it, each with a fraction of its spans and a slice of its duration.
//
// The table is named here the way metricsread.go names event.span — in the
// statement that reads it. The write half's constant is the WRITER's, and a read
// that borrows it inherits the writer's release schedule for no gain.
//
// THE WINDOW IS MEASURED ON `end`, BEFORE THE FOLD, and that is a deliberate
// trade rather than an oversight: `end` is the partition expression
// (PARTITION BY toYYYYMM(end)), so a predicate on it prunes partitions, while
// the same predicate moved into HAVING would be exact and would read the whole
// table. The cost is paid by one shape only — a trace still running when the
// window opened is summarized from the part of it inside the window, so its
// start is late and its span count low. Traces are milliseconds; this is the
// boundary row, not the common one.
//
// TENANT ISOLATION. org is the validated tenant (tenantOf), bound as a
// positional parameter, never interpolated, and the FIRST predicate — which is
// also the table's leading sort key, so the scan starts inside the tenant rather
// than filtering into it. There is no platform-sudo widening, and that is the
// one place this parts company with the RED read next door (metricsread.go): a
// RED series is an AGGREGATE over a product, so an admin seeing all of it
// discloses no tenant's records, while a trace list IS the tenant's records, one
// row at a time. A sudo bit must not turn "my traces" into "everyone's".

const (
	// The page cap. 50 fills a console list without paging; 500 is the most a
	// single answer will carry, so a caller cannot ask for the whole table.
	defaultTraceLimit = 50
	maxTraceLimit     = 500

	// The same read budget the RED query gets (metricsTimeout): one aggregate
	// over one partition-bounded scan, or the caller hears a 502.
	tracesTimeout = 10 * time.Second
)

// tracesIn bounds the list: a window, a page cap, and an optional duration
// floor. The org is the validated tenant, never a field.
type tracesIn struct {
	// Range is the window in seconds, counted back from now over each trace's
	// last activity. Default 3600, capped at 604800 (7d).
	Range int `json:"range"`
	// Limit is how many traces to return. Default 50, capped at 500.
	Limit int `json:"limit"`
	// MinDurationMs keeps only traces that lasted at least this many
	// milliseconds. Zero or absent keeps every trace in the window.
	MinDurationMs int `json:"minDurationMs"`
}

// traceRow is one trace as event.trace summarizes it. The spans themselves are
// the trace DETAIL read, GET /v1/o11y/traces/{traceId}.
type traceRow struct {
	// TraceID is the trace's id — the {traceId} of the detail read.
	TraceID string `json:"traceId"`
	// Start is the earliest span start, RFC3339 with nanoseconds, in UTC.
	Start string `json:"start"`
	// End is the latest span end, RFC3339 with nanoseconds, in UTC.
	End string `json:"end"`
	// DurationMs is End minus Start in milliseconds: the trace's wall clock,
	// not the sum of its spans, which double-counts everything concurrent.
	DurationMs float64 `json:"durationMs"`
	// NumSpans is how many spans the trace carries.
	NumSpans int64 `json:"numSpans"`
}

// tracesOut is the page, plus the bounds that produced it — so a caller can see
// which of the numbers it asked for were clamped, rather than guessing why it
// got 500 rows.
type tracesOut struct {
	// SinceSec is the window actually read, in seconds, after clamping.
	SinceSec int `json:"sinceSec"`
	// Limit is the page cap actually applied, after clamping.
	Limit int `json:"limit"`
	// Count is how many traces this page carries.
	Count int `json:"count"`
	// Traces are the caller org's traces, most recently active first.
	Traces []traceRow `json:"traces"`
}

// GetO11yTraces lists the caller org's recent traces — one row per trace with
// its span count and wall-clock duration, most recently active first. This is
// the trace SEARCH: it is where a trace id comes from, and the spans behind any
// row are then read from GET /v1/o11y/traces/{traceId}. Every row belongs to the
// caller's own org — the tenant is the validated principal, never an input, and
// there is no administrator widening, because a trace list is a tenant's records
// rather than a rollup over them. An unreachable telemetry store answers 503
// rather than an empty page, because "no traces" and "cannot see the traces" are
// different facts and only one of them is about the caller's system.
//
// Example: {"range": 3600, "limit": 50}
func handleTraces(ctx context.Context, in *tracesIn) (*tracesOut, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !datastore.Ready() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "o11y traces: datastore not connected")
	}
	q := traceListQuery{
		org:      org,
		rangeSec: boundRangeSec(in.Range),
		limit:    boundTraceLimit(in.Limit),
		// A floor, so a zero or negative value is no filter at all — enforced in
		// traceListSQL, which is the one place that decides whether it applies.
		minDurationMs: in.MinDurationMs,
	}

	rctx, cancel := context.WithTimeout(ctx, tracesTimeout)
	defer cancel()

	sql, args := traceListSQL(q)
	rows, err := datastore.Query(rctx, sql, args...)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y traces: %v", err)
	}
	out := &tracesOut{SinceSec: q.rangeSec, Limit: q.limit, Traces: make([]traceRow, 0, len(rows))}
	for _, r := range rows {
		start := asTime(r["started"]).UTC()
		end := asTime(r["ended"]).UTC()
		out.Traces = append(out.Traces, traceRow{
			TraceID: asString(r["trace_id"]),
			// RFC3339 with NANOSECONDS, unlike the bucket timestamps elsewhere in
			// this package: the column is DateTime64(9) and a trace is routinely
			// milliseconds long, so second precision would render start == end.
			Start:      start.Format(time.RFC3339Nano),
			End:        end.Format(time.RFC3339Nano),
			DurationMs: round2(float64(end.Sub(start)) / float64(time.Millisecond)),
			NumSpans:   asInt64(r["spans"]),
		})
	}
	out.Count = len(out.Traces)
	return out, nil
}

// traceListQuery is the resolved, already-authorized read: the org is the
// validated tenant and both bounds are clamped. No field carries client SQL.
type traceListQuery struct {
	org           string
	rangeSec      int
	limit         int
	minDurationMs int
}

// traceListSQL renders the statement and its positional arguments. It is a pure
// function of the clamped query so the tenant pin and both clamps can be
// measured without a warehouse — a cross-tenant leak is invisible to a handler
// test that cannot reach ClickHouse, and this is the seam where it would happen.
func traceListSQL(q traceListQuery) (string, []any) {
	// `end` is backtick-quoted because END is a SQL keyword (CASE … END); start
	// is not, so it is not quoted. The aliases are what the row scan reads, and
	// ClickHouse substitutes an alias anywhere in the query, so HAVING and ORDER
	// BY state each aggregate once.
	sql := "SELECT trace_id, min(start) AS started, max(`end`) AS ended, sum(num_spans) AS spans " +
		"FROM event.trace " +
		// THE tenant gate, on the table's leading sort-key column.
		"WHERE org = ? AND `end` > now64(9) - toIntervalSecond(?) " +
		"GROUP BY trace_id"
	args := []any{q.org, q.rangeSec}
	if q.minDurationMs > 0 {
		// The floor is post-aggregation because the duration is: a partial row
		// carries part of the trace, so no single row knows how long it ran.
		sql += " HAVING toUnixTimestamp64Milli(ended) - toUnixTimestamp64Milli(started) >= ?"
		args = append(args, int64(q.minDurationMs))
	}
	// UInt64, which is the type ClickHouse's LIMIT expression takes.
	sql += " ORDER BY ended DESC LIMIT ?"
	args = append(args, uint64(q.limit))
	return sql, args
}

// boundTraceLimit clamps the page size to [1, maxTraceLimit]. A missing or
// unparseable value arrives as 0 and takes the default — the same branch, and
// the same shape, as boundRangeSec, so the two clamps in this op behave alike.
func boundTraceLimit(n int) int {
	if n <= 0 {
		return defaultTraceLimit
	}
	if n > maxTraceLimit {
		return maxTraceLimit
	}
	return n
}
