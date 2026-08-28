package cloud

import (
	"context"
	"io"
	"testing"

	luxlog "github.com/luxfi/log"
	luxmetric "github.com/luxfi/metric"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/protobuf/proto"
)

func pushLog() luxlog.Logger { return luxlog.New("cloud").Output(io.Discard) }

// EVERY PROCESS CARRIES ITS OWN NUMBERS, AND THE BATCH SAYS WHICH APP THEY ARE.
//
// This is the half that made the store's only `product` value "o11y": exactly
// one process drained its registry, so the hundred that served everything else
// measured and published nothing, and a per-app board was empty while every app
// was serving. The push is now installed for every process, so the assertion is
// on what one push carries: the app the request was served by has to survive the
// trip into the wire's family model.
func TestOnePushCarriesTheAppThatServed(t *testing.T) {
	observeRequest("kestrel", "kestrel", "acme", 200, 0)

	var got []*luxmetric.MetricFamily
	pushMetrics(context.Background(), metricRegistry, func(_ context.Context, f []*luxmetric.MetricFamily) error {
		got = f
		return nil
	}, pushLog())

	if len(got) == 0 {
		t.Fatal("a push carried no families — this process publishes nothing")
	}
	found := false
	for _, f := range got {
		if f.Name != "hanzo_http_requests_total" {
			continue
		}
		for _, m := range f.Metrics {
			labels := map[string]string{}
			for _, l := range m.Labels {
				labels[l.Name] = l.Value
			}
			if labels["app"] == "kestrel" && labels["org"] == "acme" {
				found = true
			}
			if labels["app"] == "" {
				t.Errorf("a series left this process with no app: %v", labels)
			}
		}
	}
	if !found {
		t.Error("the series naming the serving app did not reach the wire")
	}
}

// A push that finds nothing to say says nothing. An empty batch on the wire is a
// connection, a marshal and a write that carry no measurement.
func TestAnEmptyGatherSendsNothing(t *testing.T) {
	sent := false
	empty := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) { return nil, nil })
	pushMetrics(context.Background(), empty, func(context.Context, []*luxmetric.MetricFamily) error {
		sent = true
		return nil
	}, pushLog())
	if sent {
		t.Error("an empty gather still opened a batch")
	}
}

// UNTYPED COLLAPSES TO GAUGE, and this is not cosmetic: the store's writer
// SKIPS a family whose type it does not know, so carrying "untyped" across
// would be a silent drop of every sample in it.
func TestAnUntypedFamilyTravelsAsAGauge(t *testing.T) {
	fams := familiesOf([]*dto.MetricFamily{{
		Name: proto.String("odd"),
		Type: dto.MetricType_UNTYPED.Enum(),
		Metric: []*dto.Metric{{
			Untyped: &dto.Untyped{Value: proto.Float64(7)},
		}},
	}})
	if len(fams) != 1 || fams[0].Type != luxmetric.MetricTypeGauge {
		t.Fatalf("families = %v, want one gauge", fams)
	}
	if fams[0].Metrics[0].Value.Value != 7 {
		t.Errorf("value = %v, want 7", fams[0].Metrics[0].Value.Value)
	}
}

// A histogram travels whole: count, sum and every bucket. The +Inf bucket is the
// one the exporter drops on purpose — the store's writer re-derives it from the
// count — so what leaves here is the finite buckets and the two totals.
func TestAHistogramTravelsWhole(t *testing.T) {
	fams := familiesOf([]*dto.MetricFamily{{
		Name: proto.String("latency"),
		Type: dto.MetricType_HISTOGRAM.Enum(),
		Metric: []*dto.Metric{{
			Label: []*dto.LabelPair{{Name: proto.String("app"), Value: proto.String("kestrel")}},
			Histogram: &dto.Histogram{
				SampleCount: proto.Uint64(3),
				SampleSum:   proto.Float64(1.5),
				Bucket: []*dto.Bucket{
					{UpperBound: proto.Float64(0.5), CumulativeCount: proto.Uint64(1)},
					{UpperBound: proto.Float64(1), CumulativeCount: proto.Uint64(3)},
				},
			},
		}},
	}})
	if len(fams) != 1 || fams[0].Type != luxmetric.MetricTypeHistogram {
		t.Fatalf("families = %v, want one histogram", fams)
	}
	m := fams[0].Metrics[0]
	if m.Value.SampleCount != 3 || m.Value.SampleSum != 1.5 || len(m.Value.Buckets) != 2 {
		t.Fatalf("histogram = %+v, want count 3, sum 1.5, 2 buckets", m.Value)
	}
	if len(m.Labels) != 1 || m.Labels[0].Name != "app" || m.Labels[0].Value != "kestrel" {
		t.Errorf("labels = %v, want app=kestrel", m.Labels)
	}
}

// NO PLANE, NO LEG. A deployment that runs no telemetry store has nowhere to
// send, and the honest answer is a stop function that stops nothing rather than
// a node dialling an address nothing is on.
func TestWithNoPlaneTheLegIsNotInstalled(t *testing.T) {
	t.Setenv("O11Y_DATASTORE_DSN", "")
	t.Setenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN", "")
	stop := installMetricPush(pushLog(), nil, "hanzo-cloud", "kestrel", planeMetricEndpoint)
	if stop == nil {
		t.Fatal("installMetricPush returned no stop — callers defer it unconditionally")
	}
	stop(context.Background())
}

// THE BATCH SAYS WHICH PROCESS SPOKE. Without it a hundred sibling processes
// file under one resource, which is one series written a hundred times a minute
// with ninety-nine seeded zeros in it — and every rule written over that reads a
// plane that keeps stopping and starting.
func TestABatchNamesTheProcessThatMeasured(t *testing.T) {
	res := resource.NewSchemaless(
		attribute.String("service.name", "hanzo-cloud"),
		attribute.String("deployment.environment", "prod"),
	)
	got := batchResource(res, "kestrel")

	if got["service.name"] != "hanzo-cloud" || got["deployment.environment"] != "prod" {
		t.Errorf("resource = %v — a measurement must agree with the spans and lines beside it", got)
	}
	if got["service.instance.id"] != "kestrel" {
		t.Errorf("service.instance.id = %q, want kestrel — nothing in the batch says which process spoke", got["service.instance.id"])
	}
	if other := batchResource(res, "anchor"); other["service.instance.id"] == got["service.instance.id"] {
		t.Error("two processes filed under one identity")
	}
}
