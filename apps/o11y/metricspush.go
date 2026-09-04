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

// This process's own measurements, PUSHED to the native store.
//
// Metrics used to leave here exactly one way: a Prometheus exposition on :9464
// that something outside had to come and scrape. That was never a statement
// about metrics — it was a statement about where they were KEPT, and they were
// kept in VictoriaMetrics because every reader queried VictoriaMetrics.
// VictoriaMetrics is gone. There is one telemetry store now, the datastore
// behind the o11y plane, and traces and logs already reach it in-process over
// ZAP without anybody scraping anything.
//
// So metrics take the same road as their two siblings. This is the third of
// three signals finally travelling the same way to the same place, which is
// what the transport was supposed to mean all along.
//
// WHY GATHER A PROMETHEUS REGISTRY RATHER THAN EXPORT OTel DIRECTLY.
//
// It is the shortest honest path, not a compromise. The receiver's wire shape
// (zapmetricreceiver.MetricBatch) IS the Prometheus family model — name, help,
// type, labels, value, buckets, quantiles — because that is what the collector
// on the other end has always spoken. The meter provider already renders into a
// registry in exactly that model. Gathering it is therefore a copy between two
// spellings of one structure; writing a second OTel→datastore exporter would be
// a second encoder for the same bytes, and the two would drift.
//
// The registry stops being a PUBLISHED SURFACE and becomes an internal buffer.
// Nothing binds a port for it here; nobody may scrape it. That is the whole
// change: same measurements, same shape, no listener, no puller.
//
// IN-PROCESS, not a loopback dial. The receiver on :4319 exists for OTHER
// processes; this process owns the writer, so it calls it. Sending our own
// metrics through our own socket would add a serialization, a port and a
// failure mode to reach a function already in scope — the same reasoning that
// makes O11Y_TRACES_ZAP_INPROCESS the default for spans.

package o11y

import (
	"context"
	"github.com/hanzoai/cloud/internal/environ"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/hanzoai/o11y/pkg/datastoremetrics"
	"github.com/hanzoai/o11y/pkg/telemetrystore"
	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

const (
	// metricsPushIntervalEnv overrides the push period.
	metricsPushIntervalEnv = "O11Y_METRICS_PUSH_INTERVAL"
	// defaultMetricsPushInterval matches the prober's own 30s period. Pushing
	// faster than the source changes stores duplicates, not information.
	defaultMetricsPushInterval = 30 * time.Second
	// metricsPushTimeout bounds one write. A store that is not answering must
	// cost this process one skipped push, never a wedged goroutine.
	metricsPushTimeout = 20 * time.Second
	// metricsPushAppName identifies these rows' origin in the store, beside the
	// receiver's own "cloud-o11y-metrics" node id.
	metricsPushAppName = "cloud"
)

// metricsPush is pinned for the process life so Shutdown can stop it.
var (
	metricsPushMu     sync.Mutex
	metricsPushCancel context.CancelFunc
)

// startNativeMetricsPush carries this process's measurements to the telemetry
// store on a timer.
//
// Fail-soft and self-disabling, matching every other telemetry path here: no
// store means no push and a warning, never a fatal. A process that cannot
// report its metrics must still serve its requests.
func startNativeMetricsPush(store telemetrystore.TelemetryStore, log luxlog.Logger) {
	if store == nil {
		log.Warn("native metrics push: no telemetry store; this process publishes no metrics")
		return
	}
	conn := store.Datastore()
	if conn == nil {
		log.Warn("native metrics push: datastore connection unavailable; this process publishes no metrics")
		return
	}
	interval := defaultMetricsPushInterval
	if v := environ.Or(metricsPushIntervalEnv, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else {
			log.Warn("native metrics push: unparseable interval; using default",
				"value", v, "default", interval)
		}
	}

	writer := datastoremetrics.NewWriter(conn)
	ctx, cancel := context.WithCancel(context.Background())
	metricsPushMu.Lock()
	metricsPushCancel = cancel
	metricsPushMu.Unlock()

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pushOnce(ctx, writer.WriteMetrics, log)
			}
		}
	}()
	log.Info("native metrics push running (in-process, no scrape)",
		"interval", interval, "store", "datastore")
}

// stopNativeMetricsPush ends the loop. Idempotent.
func stopNativeMetricsPush() {
	metricsPushMu.Lock()
	defer metricsPushMu.Unlock()
	if metricsPushCancel != nil {
		metricsPushCancel()
		metricsPushCancel = nil
	}
}

// writeBatch is the sink one push writes to. Named so tests substitute it
// without a datastore — the client that keeps pushOnce pure.
type writeBatch func(context.Context, *zapmetricreceiver.MetricBatch) error

// pushOnce gathers the registry and writes one batch.
//
// A gather error is REPORTED AND THEN USED: prometheus.Gather returns partial
// results alongside its error, and dropping the whole batch because one
// collector misbehaved would lose every other measurement in the process. The
// error is logged so the bad collector is findable.
func pushOnce(ctx context.Context, write writeBatch, log luxlog.Logger) {
	families, err := cloud.MetricGatherer().Gather()
	if err != nil {
		log.Warn("native metrics push: partial gather", "err", err, "families", len(families))
	}
	if len(families) == 0 {
		return
	}
	batch := batchFrom(families, time.Now())
	c, cancel := context.WithTimeout(ctx, metricsPushTimeout)
	defer cancel()
	if err := write(c, batch); err != nil {
		log.Warn("native metrics push: write failed", "err", err, "families", len(batch.Families))
	}
}

// batchFrom translates a gathered registry into the receiver's wire shape.
//
// The two models are the same structure under different names, so this is a
// rename, not a conversion — which is exactly why it is the only encoder.
func batchFrom(families []*dto.MetricFamily, now time.Time) *zapmetricreceiver.MetricBatch {
	out := &zapmetricreceiver.MetricBatch{
		AppName:     metricsPushAppName,
		TimestampNs: now.UnixNano(),
		Families:    make([]zapmetricreceiver.MetricFamily, 0, len(families)),
	}
	for _, f := range families {
		if f == nil || f.GetName() == "" {
			continue
		}
		fam := zapmetricreceiver.MetricFamily{
			Name:    f.GetName(),
			Help:    f.GetHelp(),
			Type:    familyType(f.GetType()),
			Metrics: make([]zapmetricreceiver.Metric, 0, len(f.GetMetric())),
		}
		for _, m := range f.GetMetric() {
			if m == nil {
				continue
			}
			fam.Metrics = append(fam.Metrics, metricFrom(m))
		}
		if len(fam.Metrics) == 0 {
			continue
		}
		out.Families = append(out.Families, fam)
	}
	return out
}

// familyType maps the dto enum to the receiver's lowercase vocabulary.
// UNTYPED collapses to gauge: an untyped sample is a number read at a moment,
// which is what a gauge is, and the store has no third option.
func familyType(t dto.MetricType) string {
	switch t {
	case dto.MetricType_COUNTER:
		return "counter"
	case dto.MetricType_HISTOGRAM:
		return "histogram"
	case dto.MetricType_SUMMARY:
		return "summary"
	default:
		return "gauge"
	}
}

// metricFrom carries one sample across, whichever shape it has.
func metricFrom(m *dto.Metric) zapmetricreceiver.Metric {
	out := zapmetricreceiver.Metric{Labels: labelsOf(m)}
	switch {
	case m.GetCounter() != nil:
		v := m.GetCounter().GetValue()
		out.Value = &v
	case m.GetGauge() != nil:
		v := m.GetGauge().GetValue()
		out.Value = &v
	case m.GetUntyped() != nil:
		v := m.GetUntyped().GetValue()
		out.Value = &v
	case m.GetHistogram() != nil:
		h := m.GetHistogram()
		count, sum := h.GetSampleCount(), h.GetSampleSum()
		out.SampleCount, out.SampleSum = &count, &sum
		for _, b := range h.GetBucket() {
			out.Buckets = append(out.Buckets, zapmetricreceiver.Bucket{
				UpperBound:      b.GetUpperBound(),
				CumulativeCount: b.GetCumulativeCount(),
			})
		}
	case m.GetSummary() != nil:
		s := m.GetSummary()
		count, sum := s.GetSampleCount(), s.GetSampleSum()
		out.SampleCount, out.SampleSum = &count, &sum
		for _, q := range s.GetQuantile() {
			out.Quantiles = append(out.Quantiles, zapmetricreceiver.Quantile{
				Quantile: q.GetQuantile(),
				Value:    q.GetValue(),
			})
		}
	}
	return out
}

// labelsOf flattens a sample's label pairs. Empty rather than nil when there are
// none, so the store never has to distinguish "no labels" from "unset".
func labelsOf(m *dto.Metric) map[string]string {
	pairs := m.GetLabel()
	if len(pairs) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p.GetName()] = p.GetValue()
	}
	return out
}
