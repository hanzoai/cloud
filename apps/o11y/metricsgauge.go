// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Reading a gauge back out of the native store.
//
// This is the other half of metricspush.go, and it is the piece that lets
// VictoriaMetrics go. The measurements were never the problem — the fleet
// prober has recorded hanzo_service_up for as long as it has existed. The
// problem was that the only way to READ them was a PromQL instant query against
// VM, so every reader (the public /v1/o11y/summary, the scoped /v1/o11y/status) held
// a VM client, and VM could not be removed without those endpoints going dark.
//
// Two questions are asked here and nowhere else, and they are the only two an
// availability reader has ever asked: what is the latest value of this gauge per
// label set, and what was it across a window? Answering them in ONE place is
// what keeps the callers from growing dialects of the same SQL.
//
// THE SCHEMA. Metrics land in two tables and the split matters:
//
//	event.metric  — samples: (metric_name, fingerprint, unix_milli, value)
//	event.series  — identity: (metric_name, fingerprint, labels JSON)
//
// A sample carries no labels, only a FINGERPRINT; the labels live once per
// series. So "the latest value per label set" is a join, and the join key is
// the fingerprint. Reading samples alone would give numbers nobody can
// attribute; reading series alone would give names with no values.
//
// LATEST, NOT AVERAGED. argMax(value, unix_milli) takes the value belonging to
// the newest sample rather than a mean over the window. For an availability
// gauge that distinction is the whole meaning: a service that was down for four
// of the last five minutes and is up now must read 1, and any aggregate would
// answer 0.8 — a number that is true of nothing.

package o11y

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/datastore"
)

const (
	// gaugeLookback bounds the scan. A gauge nobody has written inside this
	// window is ABSENT, not zero — the same distinction the alert rules turn
	// on, and the reason a stale reading must never be served as current.
	gaugeLookback = 10 * time.Minute
	// gaugeQueryTimeout matches the VM client's old budget, so a slow store
	// degrades a status page rather than hanging it.
	gaugeQueryTimeout = 5 * time.Second
)

// gaugeSample is one label set's newest value.
type gaugeSample struct {
	Labels map[string]string
	Value  float64
}

// latestGauge answers the newest value of a gauge per label set, from the
// native telemetry store.
//
// It replaces `newVMClient().queryInstant(name)`. The shape it returns is
// deliberately NOT the Prometheus envelope: the callers only ever destructured
// that envelope to reach exactly this, and carrying a wire format from a store
// we no longer run would be preserving a dependency's vocabulary after the
// dependency is gone.
func latestGauge(ctx context.Context, name string) ([]gaugeSample, error) {
	if name == "" {
		return nil, fmt.Errorf("latestGauge: empty metric name")
	}
	ctx, cancel := context.WithTimeout(ctx, gaugeQueryTimeout)
	defer cancel()

	// The join is on (metric_name, fingerprint): samples hold the numbers,
	// series holds the identity. GROUP BY the fingerprint so one row comes back
	// per label set, and argMax picks the value of the newest sample in it.
	const sql = `
		SELECT s.labels AS labels,
		       argMax(m.value, m.unix_milli) AS value
		FROM event.metric AS m
		INNER JOIN event.series AS s
		        ON s.fingerprint = m.fingerprint AND s.metric_name = m.metric_name
		WHERE m.metric_name = ?
		  AND m.unix_milli > toUnixTimestamp64Milli(now64(3)) - ?
		GROUP BY s.labels`

	rows, err := datastore.Query(ctx, sql, name, gaugeLookback.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("latestGauge %s: %w", name, err)
	}
	out := make([]gaugeSample, 0, len(rows))
	for _, r := range rows {
		out = append(out, gaugeSample{
			Labels: decodeLabels(r["labels"]),
			Value:  asFloat64(r["value"]),
		})
	}
	return out, nil
}

// latestGaugeBy folds latestGauge into a lookup on one label, which is how both
// callers actually use it: "is service X up?". A label a series does not carry
// is skipped rather than keyed on "", so an unlabelled series can never
// masquerade as a named one.
func latestGaugeBy(ctx context.Context, name, label string) (map[string]float64, error) {
	samples, err := latestGauge(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(samples))
	for _, s := range samples {
		if v, ok := s.Labels[label]; ok && v != "" {
			out[v] = s.Value
		}
	}
	return out, nil
}

// gaugeBucket is one time bucket of a gauge, folded across every series that
// reported inside it: the sum of their values and how many there were. For a
// 0/1 availability gauge those two numbers are "how many were up" and "how many
// answered at all" — the pair every fleet trend is drawn from.
type gaugeBucket struct {
	// StartMilli is the bucket's left edge, unix milliseconds.
	StartMilli int64
	// Sum is the sum of each series' NEWEST value inside the bucket.
	Sum float64
	// Series is how many distinct series reported inside the bucket.
	Series int
}

// gaugeSeries answers a gauge's trend over a window, bucketed at stepSec.
//
// LATEST WITHIN THE BUCKET, then summed — the same choice latestGauge makes,
// one bucket at a time, and for the same reason. A service that flapped twice
// inside one bucket has one condition at the end of it; averaging its samples
// would report a fractional service, which is true of nothing. So the inner
// query reduces each series to its newest reading in the bucket and the outer
// one folds those readings together.
//
// The join to event.series is NOT made here. The trend counts series, it does
// not name them, and the fingerprint already identifies one — so paying for the
// join would buy labels nobody reads. The instant read above is where identity
// is needed, and that is where the join lives.
//
// Buckets are computed in integer milliseconds (intDiv on the stored column)
// rather than by converting to DateTime64 first: the column IS unix
// milliseconds, so this is arithmetic on the value as stored, with no timezone
// or precision conversion to be wrong about.
func gaugeSeries(ctx context.Context, name string, rangeSec, stepSec int) ([]gaugeBucket, error) {
	if name == "" {
		return nil, fmt.Errorf("gaugeSeries: empty metric name")
	}
	if rangeSec <= 0 || stepSec <= 0 {
		return nil, fmt.Errorf("gaugeSeries %s: range and step must be positive", name)
	}
	ctx, cancel := context.WithTimeout(ctx, gaugeQueryTimeout)
	defer cancel()

	const sql = `
		SELECT bucket AS bucket,
		       sum(v) AS value_sum,
		       count() AS series_count
		FROM (
			SELECT intDiv(unix_milli, ?) * ? AS bucket,
			       fingerprint AS fingerprint,
			       argMax(value, unix_milli) AS v
			FROM event.metric
			WHERE metric_name = ?
			  AND unix_milli > toUnixTimestamp64Milli(now64(3)) - ?
			GROUP BY bucket, fingerprint
		)
		GROUP BY bucket
		ORDER BY bucket ASC`

	stepMilli := int64(stepSec) * 1000
	rangeMilli := int64(rangeSec) * 1000
	rows, err := datastore.Query(ctx, sql, stepMilli, stepMilli, name, rangeMilli)
	if err != nil {
		return nil, fmt.Errorf("gaugeSeries %s: %w", name, err)
	}
	out := make([]gaugeBucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, gaugeBucket{
			StartMilli: int64(asFloat64(r["bucket"])),
			Sum:        asFloat64(r["value_sum"]),
			Series:     int(asFloat64(r["series_count"])),
		})
	}
	return out, nil
}

// decodeLabels parses the series' label JSON. A row whose labels will not parse
// is returned EMPTY rather than dropped: the value was really measured, and the
// caller's own label lookup will skip it — which is a decision the caller
// already knows how to make.
func decodeLabels(v any) map[string]string {
	out := map[string]string{}
	s, ok := v.(string)
	if !ok || s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// asFloat64 narrows whatever numeric type the driver hands back. Datastore
// returns Float64 for these columns, but the driver's `any` may carry several
// widths depending on the column's declared type, and a status page must not
// depend on which.
func asFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	default:
		return 0
	}
}
