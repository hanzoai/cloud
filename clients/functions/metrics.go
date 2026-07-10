package functions

import (
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
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

type pointView struct {
	T string `json:"t"`
	V int    `json:"v"`
}

type seriesLine struct {
	Key    string      `json:"key"`
	Points []pointView `json:"points"`
}

type statusBreakdown struct {
	Success int `json:"success"`
	Timeout int `json:"timeout"`
	Error   int `json:"error"`
}

type metricsView struct {
	Series    []seriesLine    `json:"series"`
	Status    statusBreakdown `json:"status"`
	CostCents *int64          `json:"costCents"` // null — no per-invocation cost source
}

// buildMetrics buckets real invocation rows into a per-function series + a
// status donut. Pure over its inputs (now injectable) so it is unit-tested.
func buildMetrics(invs []Invocation, spec metricsRange, now time.Time) metricsView {
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

	series := make([]seriesLine, 0, len(perFn))
	for name, counts := range perFn {
		points := make([]pointView, spec.buckets)
		for i := 0; i < spec.buckets; i++ {
			points[i] = pointView{T: labels[i], V: counts[i]}
		}
		series = append(series, seriesLine{Key: name, Points: points})
	}
	return metricsView{Series: series, Status: st, CostCents: nil}
}

func metrics(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	spec, ok := rangeSpecs[c.Query("range")]
	if !ok {
		spec = rangeSpecs["24H"]
	}
	since := time.Now().Add(-spec.dur).Unix()
	invs, err := store.InvocationsSince(c.Context(), org, since, 5000)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	return c.JSON(http.StatusOK, buildMetrics(invs, spec, time.Now()))
}
