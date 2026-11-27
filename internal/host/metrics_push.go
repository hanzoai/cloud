// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package host

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/env"
	luxlog "github.com/luxfi/log"
	luxmetric "github.com/luxfi/metric"
	dto "github.com/prometheus/client_model/go"
)

// metricsPushEvery is how often PushMetrics ships this process's registry.
const metricsPushEvery = time.Minute

// PushMetrics ships this process's measurements to the o11y metrics receiver
// named by O11Y_METRICS_ADDR, once a minute until ctx ends.
//
// The o11y process writes its own registry to the store in-process
// (apps/o11y/metricspush.go). Every other process's registry is gathered by
// nothing, so a subsystem whose measurements must be seen — the platform cron's
// schedule gauges, read by CronScheduleMissed, CronScheduleFailing and
// BackupStale — calls this from the process that records them. Unset address,
// no push. A failed push is logged and retried next minute; it never blocks
// the caller.
//
// EVERY POD PUSHES UNDER ITS OWN NAME. The exporter's ZAP identity is the app's
// name, and the receiver admits one connection per identity, so three pods
// pushing as "cloud-workflow" would each knock the others off in turn and the
// store would hold whichever batch landed last. A pod's identity is its own and
// its batches carry it as service.instance.id, so the registries of three pods are
// three series and not one series written three times.
func PushMetrics(ctx context.Context, log luxlog.Logger, service string) {
	addr := env.Or("O11Y_METRICS_ADDR", "")
	if addr == "" {
		return
	}
	// The exporter's ZAP node identity is metric-<AppName>, and the receiver admits
	// one connection per identity, so each pushing process names itself.
	pod := coord.Default().Self()
	exp, err := luxmetric.NewZAPExporter(luxmetric.ZAPExporterConfig{
		Endpoint: addr,
		AppName:  "cloud-" + service + "-" + pod,
		Resource: map[string]string{"service.name": service, "service.instance.id": pod},
	})
	if err != nil {
		log.Warn("metrics push unavailable; this process publishes no metrics", "addr", addr, "err", err)
		return
	}
	go func() {
		defer func() { _ = exp.Shutdown(context.Background()) }()
		t := time.NewTicker(metricsPushEvery)
		defer t.Stop()
		for {
			if err := pushMetricsOnce(ctx, exp); err != nil {
				log.Warn("metrics push", "addr", addr, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	log.Info("metrics push to o11y", "addr", addr, "every", metricsPushEvery, "service", service)
}

// pushMetricsOnce gathers the registry and ships it as one batch.
func pushMetricsOnce(ctx context.Context, exp *luxmetric.ZAPExporter) error {
	fams, err := metricRegistry.Gather()
	if err != nil {
		return err
	}
	if len(fams) == 0 {
		return nil
	}
	return exp.Export(ctx, luxFamilies(fams))
}

// luxFamilies restates Prometheus families in luxfi/metric's model, which is
// what the ZAP exporter serializes and zapmetricreceiver decodes.
func luxFamilies(in []*dto.MetricFamily) []*luxmetric.MetricFamily {
	out := make([]*luxmetric.MetricFamily, 0, len(in))
	for _, f := range in {
		lf := &luxmetric.MetricFamily{Name: f.GetName(), Help: f.GetHelp(), Type: luxType(f.GetType())}
		for _, m := range f.GetMetric() {
			lm := luxmetric.Metric{}
			for _, l := range m.GetLabel() {
				lm.Labels = append(lm.Labels, luxmetric.LabelPair{Name: l.GetName(), Value: l.GetValue()})
			}
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				lm.Value.Value = m.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				lm.Value.Value = m.GetGauge().GetValue()
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				lm.Value.SampleCount, lm.Value.SampleSum = h.GetSampleCount(), h.GetSampleSum()
				for _, b := range h.GetBucket() {
					lm.Value.Buckets = append(lm.Value.Buckets, luxmetric.Bucket{UpperBound: b.GetUpperBound(), CumulativeCount: b.GetCumulativeCount()})
				}
			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				lm.Value.SampleCount, lm.Value.SampleSum = s.GetSampleCount(), s.GetSampleSum()
				for _, q := range s.GetQuantile() {
					lm.Value.Quantiles = append(lm.Value.Quantiles, luxmetric.Quantile{Quantile: q.GetQuantile(), Value: q.GetValue()})
				}
			default:
				lm.Value.Value = m.GetUntyped().GetValue()
			}
			lf.Metrics = append(lf.Metrics, lm)
		}
		out = append(out, lf)
	}
	return out
}

func luxType(t dto.MetricType) luxmetric.MetricType {
	switch t {
	case dto.MetricType_COUNTER:
		return luxmetric.MetricTypeCounter
	case dto.MetricType_GAUGE:
		return luxmetric.MetricTypeGauge
	case dto.MetricType_HISTOGRAM:
		return luxmetric.MetricTypeHistogram
	case dto.MetricType_SUMMARY:
		return luxmetric.MetricTypeSummary
	default:
		return luxmetric.MetricTypeUntyped
	}
}
