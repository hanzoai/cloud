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

package o11y

import (
	"context"
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"

	luxlog "github.com/luxfi/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
)

func f64(v float64) *float64 { return &v }
func u64(v uint64) *uint64   { return &v }

// family builds one gathered family the way the registry hands it over.
func family(name string, t dto.MetricType, ms ...*dto.Metric) *dto.MetricFamily {
	return &dto.MetricFamily{Name: proto.String(name), Type: t.Enum(), Metric: ms}
}

func labelled(pairs map[string]string) []*dto.LabelPair {
	out := make([]*dto.LabelPair, 0, len(pairs))
	for k, v := range pairs {
		out = append(out, &dto.LabelPair{Name: proto.String(k), Value: proto.String(v)})
	}
	return out
}

// TestBatchFromCarriesEveryShape — the translation is the ONE encoder between
// the registry and the store, so every shape the registry can hand over has to
// arrive intact. A gauge that silently lost its labels, or a histogram that
// arrived without buckets, would be a measurement the store cannot answer with.
func TestBatchFromCarriesEveryShape(t *testing.T) {
	now := time.Unix(1750000000, 0)
	fams := []*dto.MetricFamily{
		family("hanzo_service_up", dto.MetricType_GAUGE, &dto.Metric{
			Label: labelled(map[string]string{"service": "iam"}),
			Gauge: &dto.Gauge{Value: f64(0)},
		}),
		family("hanzo_plane_rows_written_total", dto.MetricType_COUNTER, &dto.Metric{
			Label:   labelled(map[string]string{"table": "event.span"}),
			Counter: &dto.Counter{Value: f64(8241)},
		}),
		family("hanzo_service_probe_duration_seconds", dto.MetricType_HISTOGRAM, &dto.Metric{
			Histogram: &dto.Histogram{
				SampleCount: u64(3), SampleSum: f64(0.42),
				Bucket: []*dto.Bucket{{UpperBound: f64(0.5), CumulativeCount: u64(2)}},
			},
		}),
	}

	b := batchFrom(fams, now)

	if b.AppName != metricsPushAppName {
		t.Fatalf("app name = %q", b.AppName)
	}
	if b.TimestampNs != now.UnixNano() {
		t.Fatalf("timestamp = %d, want %d", b.TimestampNs, now.UnixNano())
	}
	if len(b.Families) != 3 {
		t.Fatalf("got %d families, want 3", len(b.Families))
	}

	// The gauge whose ZERO is the whole point: hanzo_service_up == 0 is a real
	// negative answer, and a translation that dropped a zero value would turn a
	// down service into a missing one.
	up := b.Families[0]
	if up.Name != "hanzo_service_up" || up.Type != "gauge" {
		t.Fatalf("gauge family = %+v", up)
	}
	if up.Metrics[0].Value == nil || *up.Metrics[0].Value != 0 {
		t.Fatalf("a zero gauge did not survive: %+v", up.Metrics[0].Value)
	}
	if up.Metrics[0].Labels["service"] != "iam" {
		t.Fatalf("labels lost: %+v", up.Metrics[0].Labels)
	}

	if c := b.Families[1]; c.Type != "counter" || *c.Metrics[0].Value != 8241 {
		t.Fatalf("counter family = %+v", c)
	}

	h := b.Families[2]
	if h.Type != "histogram" {
		t.Fatalf("histogram type = %q", h.Type)
	}
	if h.Metrics[0].SampleCount == nil || *h.Metrics[0].SampleCount != 3 {
		t.Fatalf("histogram count lost")
	}
	if len(h.Metrics[0].Buckets) != 1 || h.Metrics[0].Buckets[0].CumulativeCount != 2 {
		t.Fatalf("histogram buckets lost: %+v", h.Metrics[0].Buckets)
	}
}

// An untyped sample is a number read at a moment, which is a gauge. The store
// has no third option, so collapsing is correct — and pinned, because silently
// dropping untyped families would lose whatever a linked library registered.
func TestUntypedBecomesGauge(t *testing.T) {
	b := batchFrom([]*dto.MetricFamily{
		family("odd", dto.MetricType_UNTYPED, &dto.Metric{Untyped: &dto.Untyped{Value: f64(7)}}),
	}, time.Now())
	if len(b.Families) != 1 || b.Families[0].Type != "gauge" {
		t.Fatalf("untyped did not collapse to gauge: %+v", b.Families)
	}
	if *b.Families[0].Metrics[0].Value != 7 {
		t.Fatalf("untyped value lost")
	}
}

// Families with no samples are dropped rather than written empty: a row that
// names a metric and carries no measurement is not a measurement.
func TestEmptyFamiliesAreDropped(t *testing.T) {
	b := batchFrom([]*dto.MetricFamily{
		family("nothing", dto.MetricType_GAUGE),
		nil,
		family("", dto.MetricType_GAUGE, &dto.Metric{Gauge: &dto.Gauge{Value: f64(1)}}),
	}, time.Now())
	if len(b.Families) != 0 {
		t.Fatalf("empty/unnamed families survived: %+v", b.Families)
	}
}

// TestPushOnceReachesTheSink drives the WHOLE real path end to end: install the
// process meter provider, record through the ordinary OTel API, then push. What
// arrives at the sink is what a scrape would have collected — which is the
// proof the scrape is no longer needed.
func TestPushOnceReachesTheSink(t *testing.T) {
	shutdown := cloud.InstallTelemetry(context.Background(), luxlog.New("test"), "cloud-test")
	t.Cleanup(func() { shutdown(context.Background()) })

	// A gauge recorded exactly as probes.go records hanzo_service_up, including
	// the value that matters most: ZERO, a real negative answer.
	g, err := otel.Meter("test").Int64Gauge("hanzo_test_service_up")
	if err != nil {
		t.Fatalf("gauge: %v", err)
	}
	g.Record(context.Background(), 0, metric.WithAttributes(attribute.String("service", "iam")))

	var got *zapmetricreceiver.MetricBatch
	pushOnce(context.Background(), func(_ context.Context, b *zapmetricreceiver.MetricBatch) error {
		got = b
		return nil
	}, luxlog.New("test"))

	if got == nil {
		t.Fatal("nothing reached the sink — the process's metrics have no exit")
	}
	if got.AppName != metricsPushAppName || got.TimestampNs == 0 {
		t.Fatalf("batch envelope not stamped: %+v", got)
	}
	var found *zapmetricreceiver.Metric
	for _, f := range got.Families {
		if f.Name == "hanzo_test_service_up" && len(f.Metrics) > 0 {
			found = &f.Metrics[0]
		}
	}
	if found == nil {
		names := make([]string, 0, len(got.Families))
		for _, f := range got.Families {
			names = append(names, f.Name)
		}
		t.Fatalf("the recorded gauge never reached the store; carried: %v", names)
	}
	if found.Value == nil || *found.Value != 0 {
		t.Fatalf("the zero did not survive the trip: %+v", found.Value)
	}
	if found.Labels["service"] != "iam" {
		t.Fatalf("attributes did not become labels: %+v", found.Labels)
	}
}

// A store that refuses a write must cost one skipped push, never a panic and
// never a wedged loop. Telemetry is not allowed to take the process down.
func TestPushOnceSurvivesAFailingSink(t *testing.T) {
	pushOnce(context.Background(), func(context.Context, *zapmetricreceiver.MetricBatch) error {
		return errors.New("datastore unavailable")
	}, luxlog.New("test"))
}

// stopNativeMetricsPush is idempotent — a second Shutdown must not panic on an
// already-cancelled loop.
func TestStopIsIdempotent(t *testing.T) {
	stopNativeMetricsPush()
	stopNativeMetricsPush()
}
