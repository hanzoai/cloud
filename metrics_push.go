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

// The METRIC leg of the telemetry plane — the third signal, installed the same
// way as the other two.
//
// A meter provider is only a place to put numbers. Something has to carry them,
// and until now exactly ONE process carried its own: the o11y binary drained its
// registry straight into the store because it holds the datastore connection.
// The other hundred-odd plugin processes measured everything and published
// nothing — every counter they incremented lived and died in their own heap,
// which is why the only value the store ever held for `product` was o11y's, and
// why a per-app board could be empty while every app was serving.
//
// So the leg installs where the LOG leg installs: in InstallTelemetry, which is
// the one bootstrap every composition root calls. A process that has telemetry
// has this, and there is no per-program line to forget.
//
// WHY THE REGISTRY AND NOT AN OTel EXPORT. The receiver's wire shape is the
// Prometheus family model — name, help, type, labels, value, buckets, quantiles
// — because that is what the store's writer decomposes. The meter provider
// already renders into a registry in exactly that model (telemetry.go's
// installMeter). Gathering it is a copy between two spellings of one structure;
// a second OTel→plane exporter would be a second encoder for the same bytes and
// the two would drift.
//
// WHAT THIS FILE OWNS AND WHAT IT DOES NOT. It owns one translation — a
// gathered Prometheus family into luxfi/metric's family, which is a rename of
// fields — and nothing else. The envelope, the message type and the JSON on the
// wire belong to luxfi/metric, whose exporter is the sending half of the
// receiver apps/o11y binds. There is no hand-rolled frame here for the same
// reason there is none in the log leg.
//
// THE PROCESS NAMES ITSELF, and that is what makes a hundred senders into a
// hundred series instead of one contested one. A metric series is identified by
// its resource plus its labels, so a hundred processes reporting the same
// resource would write the SAME series a hundred times a minute with a hundred
// different values — one real and ninety-nine seeded zeros, interleaved, and
// every rule written over them wrong. service.instance.id is OTel's own name for
// which process of a service this is, and it is the app the process serves, so
// the answer is stable across restarts (a pid is not) and bounded by the
// manifest (a hostname is not).

package cloud

import (
	"context"
	"os"
	"strconv"
	"time"

	luxlog "github.com/luxfi/log"
	luxmetric "github.com/luxfi/metric"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/sdk/resource"
)

// planeMetricEndpoint is the plane ingest's ZAP METRIC wire on the pod's
// loopback — the address apps/o11y binds (planeMetricListen, 0.0.0.0:4319).
//
// It is NAMED BY THE CALLER and never defaulted to, for the reason the log
// endpoint states: luxfi/metric's exporter defaults to 127.0.0.1:4317, which is
// the SPAN wire — a metric batch sent there meets a receiver that decodes span
// envelopes, and the whole leg reads as configured while delivering nothing.
// Three ports because three receivers.
const planeMetricEndpoint = "127.0.0.1:4319"

const (
	// metricPushInterval is how often a process states its numbers. It matches
	// the fleet prober's own period: pushing faster than the source changes
	// stores duplicates, not information.
	metricPushInterval = 30 * time.Second

	// metricPushTimeout bounds one export. A plane that is not answering must
	// cost this process one skipped push, never a wedged goroutine.
	metricPushTimeout = 20 * time.Second
)

// installMetricPush starts this process's metric leg and returns the
// flush-and-stop. Called ONLY from InstallTelemetry, which is where the service
// name and the resource a batch is filed under are resolved — the same rule the
// log leg keeps, so a line, a span and a measurement agree about what produced
// them.
//
// service is the SUBJECT (what this is: hanzo-cloud) and instance is WHICH
// PROCESS of it (the app it serves). The transport's own identity carries the
// pid on top of both, because ZAP admits one connection per node id while this
// binary re-execs itself as many sibling processes — the losers of that race
// drop their batches silently, which is the same collision the span and log
// wires each solved the same way.
func installMetricPush(log luxlog.Logger, res *resource.Resource, service, instance, endpoint string) func(context.Context) {
	if PlaneDSN() == "" {
		log.Info("measurements stay in the process: this deployment runs no telemetry plane to carry them",
			"hint", "O11Y_DATASTORE_DSN is the same fact that binds the plane's metric ear")
		return func(context.Context) {}
	}

	exp, err := luxmetric.NewZAPExporter(luxmetric.ZAPExporterConfig{
		Endpoint: endpoint,
		AppName:  service + "-" + strconv.Itoa(os.Getpid()),
		Resource: batchResource(res, instance),
	})
	if err != nil {
		log.Warn("metric wire unavailable; this process publishes no measurements",
			"endpoint", endpoint, "err", err)
		return func(context.Context) {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		t := time.NewTicker(metricPushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pushMetrics(ctx, metricRegistry, exp.Export, log)
			}
		}
	}()
	log.Info("metric wire installed", "endpoint", endpoint, "service", service, "instance", instance)

	return func(ctx context.Context) {
		cancel()
		// The last interval, on the way out. A process that stops between ticks
		// would otherwise take its final numbers with it — including the ones
		// that say why it stopped.
		pushMetrics(ctx, metricRegistry, exp.Export, log)
		if err := exp.Shutdown(ctx); err != nil {
			log.Warn("metric wire shutdown", "err", err)
		}
	}
}

// batchResource is what every batch from this process is filed under: the
// process resource — the same value its spans and its log lines carry, so the
// three signals agree about what produced them — plus which process of the
// service this is.
//
// THE INSTANCE IS THE WHOLE OF WHY A HUNDRED SENDERS DO NOT COLLIDE. A series is
// identified by its resource and its labels, and nearly every series here is
// seeded at zero in EVERY process (metrics_plane.go says why). Filed under one
// resource, a hundred processes would write one series a hundred times a minute
// — ninety-nine zeros and one real value, interleaved — and every rule over it
// would read a plane that keeps stopping and starting.
func batchResource(res *resource.Resource, instance string) map[string]string {
	var attrs map[string]string
	if res != nil {
		attrs = make(map[string]string, len(res.Attributes())+1)
		for _, kv := range res.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
	} else {
		attrs = make(map[string]string, 1)
	}
	if instance != "" {
		attrs["service.instance.id"] = instance
	}
	return attrs
}

// export is the wire one push writes to. Named so a test substitutes it without
// a plane — the client that keeps pushMetrics pure.
type export func(context.Context, []*luxmetric.MetricFamily) error

// pushMetrics gathers the registry and ships one batch.
//
// A gather error is REPORTED AND THEN USED: Gather returns partial results
// alongside its error, and dropping the whole batch because one collector
// misbehaved would lose every other measurement in the process. The error is
// logged so the bad collector is findable.
func pushMetrics(ctx context.Context, g prometheus.Gatherer, send export, log luxlog.Logger) {
	families, err := g.Gather()
	if err != nil {
		log.Warn("metric wire: partial gather", "err", err, "families", len(families))
	}
	out := familiesOf(families)
	if len(out) == 0 {
		return
	}
	c, cancel := context.WithTimeout(ctx, metricPushTimeout)
	defer cancel()
	if err := send(c, out); err != nil {
		log.Warn("metric wire: export failed", "err", err, "families", len(out))
	}
}

// familiesOf renames a gathered registry into the wire's family model. The two
// are the same structure under different spellings, so this is a rename and not
// a conversion — which is exactly why it is the only encoder in this direction.
func familiesOf(families []*dto.MetricFamily) []*luxmetric.MetricFamily {
	out := make([]*luxmetric.MetricFamily, 0, len(families))
	for _, f := range families {
		if f == nil || f.GetName() == "" {
			continue
		}
		fam := &luxmetric.MetricFamily{
			Name:    f.GetName(),
			Help:    f.GetHelp(),
			Type:    familyType(f.GetType()),
			Metrics: make([]luxmetric.Metric, 0, len(f.GetMetric())),
		}
		for _, m := range f.GetMetric() {
			if m == nil {
				continue
			}
			fam.Metrics = append(fam.Metrics, metricOf(m))
		}
		if len(fam.Metrics) == 0 {
			continue
		}
		out = append(out, fam)
	}
	return out
}

// familyType maps the gathered enum onto the wire's vocabulary. UNTYPED
// collapses to gauge: an untyped sample is a number read at a moment, which is
// what a gauge is, and the store's writer SKIPS a family whose type it does not
// know — so carrying "untyped" through would be a silent drop.
func familyType(t dto.MetricType) luxmetric.MetricType {
	switch t {
	case dto.MetricType_COUNTER:
		return luxmetric.MetricTypeCounter
	case dto.MetricType_HISTOGRAM:
		return luxmetric.MetricTypeHistogram
	case dto.MetricType_SUMMARY:
		return luxmetric.MetricTypeSummary
	default:
		return luxmetric.MetricTypeGauge
	}
}

// metricOf carries one sample across, whichever shape it has.
func metricOf(m *dto.Metric) luxmetric.Metric {
	out := luxmetric.Metric{Labels: labelsOf(m)}
	switch {
	case m.GetCounter() != nil:
		out.Value.Value = m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		out.Value.Value = m.GetGauge().GetValue()
	case m.GetUntyped() != nil:
		out.Value.Value = m.GetUntyped().GetValue()
	case m.GetHistogram() != nil:
		h := m.GetHistogram()
		out.Value.SampleCount, out.Value.SampleSum = h.GetSampleCount(), h.GetSampleSum()
		for _, b := range h.GetBucket() {
			out.Value.Buckets = append(out.Value.Buckets, luxmetric.Bucket{
				UpperBound:      b.GetUpperBound(),
				CumulativeCount: b.GetCumulativeCount(),
			})
		}
	case m.GetSummary() != nil:
		s := m.GetSummary()
		out.Value.SampleCount, out.Value.SampleSum = s.GetSampleCount(), s.GetSampleSum()
		for _, q := range s.GetQuantile() {
			out.Value.Quantiles = append(out.Value.Quantiles, luxmetric.Quantile{
				Quantile: q.GetQuantile(),
				Value:    q.GetValue(),
			})
		}
	}
	return out
}

// labelsOf carries a sample's label pairs across in the order they were
// gathered, which is sorted — so equal samples render equal bytes and the
// store's series fingerprint is stable across processes.
func labelsOf(m *dto.Metric) []luxmetric.LabelPair {
	pairs := m.GetLabel()
	if len(pairs) == 0 {
		return nil
	}
	out := make([]luxmetric.LabelPair, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, luxmetric.LabelPair{Name: p.GetName(), Value: p.GetValue()})
	}
	return out
}
