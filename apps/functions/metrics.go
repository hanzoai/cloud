package functions

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// metricsRange defines a chart window: its total duration and how many buckets
// the invocation series is split into. Every point is a REAL count of rows that
// fell in that bucket — nothing is interpolated or invented.
type metricsRange struct {
	dur     time.Duration
	buckets int
}

var rangeSpecs = map[string]metricsRange{
	"1H":  {time.Hour, 12},
	"6H":  {6 * time.Hour, 12},
	"24H": {24 * time.Hour, 24},
	"7D":  {7 * 24 * time.Hour, 7},
	"30D": {30 * 24 * time.Hour, 30},
}

// pointView is one bucket of the invocation series.
type pointView struct {
	T string `json:"t"` // the bucket's start, RFC3339 (UTC)
	V int    `json:"v"` // how many invocations fell in it — a real count, never interpolated
}

// costLine is one function's invocation series over the window.
type costLine struct {
	Key    string      `json:"key"`    // the function the line is about
	Points []pointView `json:"points"` // one point per bucket, oldest first
}

// statusBreakdown is how the window's invocations ended.
type statusBreakdown struct {
	Success int `json:"success"` // invocations whose code ran and wrote nothing to stderr
	Timeout int `json:"timeout"` // invocations that hit their configured deadline
	Error   int `json:"error"`   // invocations that ran and failed
}

// usage is the functions dashboard for one window.
type usage struct {
	Series    []costLine      `json:"series"`    // one line per function that ran in the window
	Status    statusBreakdown `json:"status"`    // how those invocations ended
	CostCents *int64          `json:"costCents"` // null — no per-invocation cost source
}

// metricsQuery is the window a dashboard read covers.
type metricsQuery struct {
	// Range is 1H, 6H, 24H (the default), 7D or 30D. Anything else falls back to
	// 24H rather than failing.
	Range string `json:"range"`
}

// buildMetrics buckets real invocation rows into a per-function series + a
// status donut. Pure over its inputs (now injectable) so it is unit-tested.
func buildMetrics(invs []Invocation, spec metricsRange, now time.Time) usage {
	start := now.Add(-spec.dur)
	bucketDur := spec.dur / time.Duration(spec.buckets)

	// Bucket edges (RFC3339 labels) computed once.
	labels := make([]string, spec.buckets)
	for i := 0; i < spec.buckets; i++ {
		labels[i] = start.Add(time.Duration(i) * bucketDur).UTC().Format(time.RFC3339)
	}

	perFn := map[string][]int{}
	var st statusBreakdown
	for _, iv := range invs {
		t := time.Unix(iv.CreatedAt, 0)
		if t.Before(start) || t.After(now) {
			continue
		}
		switch iv.Status {
		case "ok":
			st.Success++
		case "timeout":
			st.Timeout++
		default:
			st.Error++
		}
		idx := int(t.Sub(start) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= spec.buckets {
			idx = spec.buckets - 1
		}
		row, ok := perFn[iv.FunctionName]
		if !ok {
			row = make([]int, spec.buckets)
		}
		row[idx]++
		perFn[iv.FunctionName] = row
	}

	series := make([]costLine, 0, len(perFn))
	for name, counts := range perFn {
		points := make([]pointView, spec.buckets)
		for i := 0; i < spec.buckets; i++ {
			points[i] = pointView{T: labels[i], V: counts[i]}
		}
		series = append(series, costLine{Key: name, Points: points})
	}
	return usage{Series: series, Status: st, CostCents: nil}
}

// metrics is the org's serverless dashboard over a window: a per-function
// invocation costLine and how those invocations ended.
//
// Every point is a REAL count of rows that fell in that bucket — nothing is
// interpolated or invented, so an empty window draws a flat line rather than a
// fabricated one.
//
// costCents is null and stays null: there is no per-invocation cost source to read,
// and reporting a number computed some other way would be a guess presented as a
// measurement. Requires a validated principal; the read is scoped to its org.
func (o ops) metrics(ctx context.Context, in *metricsQuery) (*usage, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	spec, ok := rangeSpecs[in.Range]
	if !ok {
		spec = rangeSpecs["24H"]
	}
	since := time.Now().Add(-spec.dur).Unix()
	invs, err := store.InvocationsSince(ctx, org, since, 5000)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	out := buildMetrics(invs, spec, time.Now())
	return &out, nil
}
