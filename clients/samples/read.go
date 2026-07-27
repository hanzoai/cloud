package samples

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/hanzoai/cloud/clients/principal"
)

// read.go is the series' read face: two questions, two queries.
//
//   - Series — "how did this unit look over the last N?"  (the chart)
//   - Latest — "how does every unit look right now?"      (the board's overlay)
//
// Both are built by PURE functions (buildSeries / buildLatest) so the tenancy and
// injection properties are unit-testable without a warehouse. The rules they hold:
// org leads the WHERE as a BOUND parameter and is never optional; every caller
// value is bound; `range` and `source` select from a CLOSED allowlist and are
// never string-built; and the scan is always bounded by both a time predicate and
// a LIMIT.

// Query is a resolved, already-authorized read. Org is the tenant and is REQUIRED
// — the controller fills it from the validated principal, never from a client
// field. Unit/Source narrow within that tenant; Range is an allowlisted label.
type Query struct {
	Org    string
	Unit   string
	Source string
	Range  string
}

// ranges is the CLOSED set of windows a caller may ask for. The label is a KEY
// into this map, never text that reaches a statement: an unknown or hostile
// ?range can therefore only ever resolve to the default. 90d is the raw table's
// full TTL horizon.
var ranges = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// DefaultRange is the window a read gets when ?range is absent or unrecognized.
const DefaultRange = "24h"

// maxRows bounds a Series read. A SERVER constant (never caller input), so
// rendering it into the statement is injection-safe by construction. 24h of
// per-minute heartbeats is ~1440 rows per unit; this holds a month of them.
const maxRows = 50000

// maxUnits bounds a Latest read — the number of distinct compute units one org's
// board can render. Also a server constant.
const maxUnits = 5000

// latestWindow bounds the Latest scan. A sample older than this is not "current
// utilization" for a live board; the full history stays available through Series.
const latestWindow = 24 * time.Hour

// since resolves a range label to its window start. Unknown → the default.
func since(label string) time.Time {
	d, ok := ranges[strings.TrimSpace(label)]
	if !ok {
		d = ranges[DefaultRange]
	}
	return time.Now().UTC().Add(-d)
}

// org validates the tenant key for a READ. Fails closed on blank/oversized —
// identical to the write's rule, so neither side can be entered without a tenant.
func org(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > principal.MaxOrgLen {
		return "", errOrg
	}
	return v, nil
}

// buildSeries assembles the time-series read. Pure, so the isolation and
// injection properties are proven by unit test. org is bound FIRST and is
// unconditional; unit/source are bound narrowers WITHIN that tenant, so no
// combination of them can ever widen the scan past the caller's own org.
func buildSeries(q Query) (string, []any, error) {
	o, err := org(q.Org)
	if err != nil {
		return "", nil, err
	}
	sql := `SELECT ` + cols + ` FROM ` + table + ` WHERE org = ? AND ts >= ?`
	args := []any{o, since(q.Range)}
	if u := strings.TrimSpace(q.Unit); u != "" {
		if len(u) > maxUnit {
			return "", nil, errUnit
		}
		sql += ` AND unit = ?`
		args = append(args, u)
	}
	if s := strings.ToLower(strings.TrimSpace(q.Source)); s != "" {
		if !validSource(s) {
			return "", nil, errSource
		}
		sql += ` AND source = ?`
		args = append(args, s)
	}
	sql += ` ORDER BY ts ASC LIMIT ` + strconv.Itoa(maxRows)
	return sql, args, nil
}

// buildLatest assembles the board's overlay: each of the org's units with its most
// recent sample. `LIMIT 1 BY unit` over the (org, unit, ts) ordering is the
// engine's own "latest per key" — one bounded pass, no self-join.
func buildLatest(o string) (string, []any, error) {
	v, err := org(o)
	if err != nil {
		return "", nil, err
	}
	sql := `SELECT ` + cols + ` FROM ` + table + ` WHERE org = ? AND ts >= ?` +
		` ORDER BY unit ASC, ts DESC LIMIT 1 BY unit LIMIT ` + strconv.Itoa(maxUnits)
	return sql, []any{v, time.Now().UTC().Add(-latestWindow)}, nil
}

// Series returns the org's samples over a bounded window, oldest first — the data
// behind a utilization chart. A blank org fails closed; an absent datastore is an
// honest empty (never fabricated zeros).
func Series(ctx context.Context, q Query) ([]Sample, error) {
	sql, args, err := buildSeries(q)
	if err != nil {
		return nil, err
	}
	if !datastore.Ready() {
		return []Sample{}, nil
	}
	rows, err := datastore.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleFrom(r))
	}
	return out, nil
}

// Latest returns each of the org's units keyed by unit id, with its most recent
// sample inside latestWindow. A blank org fails closed; an absent datastore is an
// honest empty map, which a board renders as "no samples yet" rather than failing.
func Latest(ctx context.Context, o string) (map[string]Sample, error) {
	sql, args, err := buildLatest(o)
	if err != nil {
		return nil, err
	}
	if !datastore.Ready() {
		return map[string]Sample{}, nil
	}
	rows, err := datastore.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Sample, len(rows))
	for _, r := range rows {
		s := sampleFrom(r)
		if s.Unit == "" {
			continue
		}
		out[s.Unit] = s
	}
	return out, nil
}

// sampleFrom rebuilds a Sample from one DatastoreQuery row. The driver decodes
// each column to its own native Go type (uint16/uint64/float32/time.Time/…); the
// coercers below accept those natives, so a driver or transport change degrades to
// a zero value rather than crashing a read.
func sampleFrom(r map[string]any) Sample {
	return Sample{
		Org:       str(r["org"]),
		Source:    str(r["source"]),
		Unit:      str(r["unit"]),
		Host:      str(r["host"]),
		Kind:      str(r["kind"]),
		At:        ts(r["ts"]),
		CPUs:      int(i64(r["cpus"])),
		Memory:    i64(r["memory"]),
		MemUsed:   i64(r["mem_used"]),
		MemFree:   i64(r["mem_free"]),
		Load1:     f64(r["load1"]),
		Load5:     f64(r["load5"]),
		Load15:    f64(r["load15"]),
		GPUUtil:   f64(r["gpu_util"]),
		GPUs:      int(i64(r["gpus"])),
		GPUModel:  str(r["gpu_model"]),
		CostCents: i64(r["cost_cents"]),
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func ts(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return time.Time{}
}

func i64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func f64(v any) float64 {
	switch n := v.(type) {
	case float32:
		return float64(n)
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	default:
		return 0
	}
}
