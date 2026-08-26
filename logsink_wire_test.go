package cloud

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/o11y/pkg/zaplogreceiver"
	luxlog "github.com/luxfi/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// THE HOP ITSELF, over the real wire, into the real ear.
//
// Everything either side of this is already pinned — the parser by
// logsink_test.go, the row by apps/o11y's logRowsOf tests — and both halves
// would keep passing while the leg between them delivered nothing, which is the
// exact shape of the four and a half months of dark span ingest. So this test
// speaks the production transport (luxfi/log's ZAP exporter) to the production
// receiver (the one apps/o11y binds), and meets those tests at the ONE shape
// both sides name: zaplogreceiver.LogBatch.
//
// It listens on a SOCKET in the test's own directory rather than the plane's
// port, because the port belongs to whatever o11y is running on this machine.
func TestALoggedLineReachesThePlanesEar(t *testing.T) {
	var mu sync.Mutex
	var got []*zaplogreceiver.LogBatch
	sock := filepath.Join(t.TempDir(), "logs.sock")

	rcv, err := zaplogreceiver.New(zaplogreceiver.Config{
		Listen: sock,
		NodeID: "logsink-wire-test",
		OnBatch: func(_ context.Context, b *zaplogreceiver.LogBatch) error {
			mu.Lock()
			got = append(got, b)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("plane log ear: %v", err)
	}
	t.Cleanup(rcv.Stop)

	t.Setenv("O11Y_DATASTORE_DSN", "tcp://127.0.0.1:9000")
	res := resource.NewSchemaless(attribute.String("service.name", "hanzo-cloud"))

	sink := planeLog
	planeLog = &logSink{}
	t.Cleanup(func() { planeLog = sink })

	stop := installLogSink(luxlog.New("cloud").Output(io.Discard), res, "hanzo-cloud", sock)

	const trace, span = "42a955c492853ae712c26509654b127f", "ee90855933048377"
	luxlog.New("cloud").Output(luxlog.MultiLevelWriter(io.Discard, planeLog)).
		Error("request", "module", "zip", "path", "/v1/x", "status", 503,
			"trace", trace, "span", span)

	// Shutdown is what flushes the batch processor, so the assertion below runs
	// against a wire that has finished rather than one that is still deciding.
	stop(context.Background())

	// A BOUND SHORTER THAN THE PROCESSOR'S OWN EXPORT INTERVAL (one second), so
	// arrival here proves the shutdown FLUSHED rather than the timer happening to
	// fire. Without the flush a process loses whatever it logged in its last
	// second, which is the second anyone reads after a crash.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("nothing reached the ear — the leg is dark")
	}
	b := got[0]
	if b.Resource["service.name"] != "hanzo-cloud" {
		t.Errorf("resource service.name = %q, want the telemetry's subject: it is what the plane files the row under",
			b.Resource["service.name"])
	}
	if b.AppName == "hanzo-cloud" {
		t.Error("the transport's identity is the service name — ZAP admits one connection per node id, " +
			"and ~110 sibling processes read the same service name")
	}
	if len(b.Records) != 1 {
		t.Fatalf("batch carries %d records, want 1", len(b.Records))
	}
	r := b.Records[0]
	if r.Body != "request" || r.SeverityText != "error" {
		t.Errorf("record = %q/%q, want the line that was logged", r.Body, r.SeverityText)
	}
	if r.TraceID != trace || r.SpanID != span {
		t.Errorf("ids = %s/%s, want %s/%s", r.TraceID, r.SpanID, trace, span)
	}
	if r.Attributes["path"] != "/v1/x" {
		t.Errorf("path = %v, want /v1/x", r.Attributes["path"])
	}
	if status, _ := r.Attributes["status"].(float64); status != 503 {
		t.Errorf("status = %#v, want 503 as a number", r.Attributes["status"])
	}
	if r.TimeUnixNs == 0 {
		t.Error("record carries no time — the plane would stamp it with arrival instead")
	}
}

// A deployment with no telemetry store has no ear to send to, so the leg closes
// rather than holding a boot window it can never spend or dialling an address
// nothing answers.
func TestNoStoreMeansNoLeg(t *testing.T) {
	t.Setenv("O11Y_DATASTORE_DSN", "")
	t.Setenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN", "")

	sink := planeLog
	planeLog = &logSink{}
	t.Cleanup(func() { planeLog = sink })

	stop := installLogSink(luxlog.New("cloud").Output(io.Discard), resource.NewSchemaless(), "hanzo-cloud", "127.0.0.1:1")
	t.Cleanup(func() { stop(context.Background()) })

	_, _ = planeLog.Write([]byte(`{"level":"info","message":"after"}`))
	if n := holding(planeLog); n != 0 {
		t.Errorf("holding %d lines with nowhere to send them", n)
	}
}

// PlaneDSN is the one answer to "is there a plane here", and apps/o11y binds the
// ear on the same fact. Two readers of one truth; a second reader with its own
// spelling is how a sender and its receiver disagree about whether they exist.
func TestThePlaneIsOneFact(t *testing.T) {
	t.Setenv("O11Y_DATASTORE_DSN", "")
	t.Setenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN", "")
	if got := PlaneDSN(); got != "" {
		t.Errorf("PlaneDSN = %q with neither set, want empty", got)
	}
	t.Setenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN", "tcp://store:9000")
	if got := PlaneDSN(); got != "tcp://store:9000" {
		t.Errorf("PlaneDSN = %q, want the module's own spelling to count", got)
	}
	t.Setenv("O11Y_DATASTORE_DSN", "tcp://first:9000")
	if got := PlaneDSN(); got != "tcp://first:9000" {
		t.Errorf("PlaneDSN = %q, want the deployment's own setting to win", got)
	}
}
