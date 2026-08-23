package event

// bus_test.go — the plane's ONE name, and what the ensure does when an EARLIER one
// is still holding its subjects.
//
// These run against a REAL embedded JetStream, not a fake, because the whole defect
// lives in JetStream's own rule: it reconciles a stream by NAME and binds subjects by
// OWNERSHIP, so a renamed plane deadlocks on its own predecessor. A stub that returned
// a canned overlap error would prove nothing about that rule — it would only prove the
// stub returns what the test told it to.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/commerce/infra"
	"github.com/hanzoai/pubsub-go/jetstream"
	psembed "github.com/hanzoai/pubsub/embed"
)

// planeOn starts an embedded server on an ephemeral port and returns its URL and a client
// on it. Ephemeral so tests can run in parallel with each other and with a developer's
// own cloud on 4222.
func planeOn(t *testing.T) (string, *infra.PubSubClient) {
	t.Helper()
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown() })
	cl, err := infra.NewPubSubClient(context.Background(), &infra.PubSubConfig{
		URL: srv.ClientURL(), Name: "bus-test", EnableJetStream: true,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return srv.ClientURL(), cl
}

// squat creates a stream under `name` that holds the plane's subjects — the shape of
// every stale generation, and of a tenant endpoint that regressed.
func squat(t *testing.T, js jetstream.JetStream, name string) {
	t.Helper()
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name: name, Subjects: EventSubjects, Storage: jetstream.FileStorage,
	}); err != nil {
		t.Fatalf("seed stream %s: %v", name, err)
	}
}

// TestEnsureRetiresAnEarlierGeneration is THE regression, written from the outage:
// EVENTS — the name apps/webhooks used before analytics became the plane's one owner —
// still held event.>, so EVENT could never bind and every POST /v1/event answered 503
// "subjects overlap with an existing stream". Durably: the store outlives the pod, so
// no restart, redeploy or rollback cleared it.
func TestEnsureRetiresAnEarlierGeneration(t *testing.T) {
	_, cl := planeOn(t)
	ctx := context.Background()
	js := cl.JetStream()
	squat(t, js, "EVENTS")

	// Prove the deadlock is REAL before proving it is survived — otherwise a passing
	// test could just mean the overlap never happened.
	if _, err := js.CreateOrUpdateStream(ctx, eventStream); !overlaps(err) {
		t.Fatalf("want a subject-overlap refusal from the plain ensure, got %v", err)
	}

	if err := EnsureEventStream(ctx, cl); err != nil {
		t.Fatalf("ensure must retire the earlier generation and bind: %v", err)
	}

	st, err := js.Stream(ctx, EventStream)
	if err != nil {
		t.Fatalf("%s must exist after the ensure: %v", EventStream, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if got := info.Config.Subjects; len(got) != 1 || got[0] != EventSubjects[0] {
		t.Fatalf("%s binds %v, want %v", EventStream, got, EventSubjects)
	}
	// And the configuration is the one THIS package declares, not whatever the stale
	// stream happened to carry — a retire that left the old limits in place would be
	// an unbounded plane wearing the right name.
	if info.Config.MaxAge != streamAge || info.Config.MaxBytes != streamBytes {
		t.Fatalf("%s has age=%v bytes=%d, want age=%v bytes=%d",
			EventStream, info.Config.MaxAge, info.Config.MaxBytes, streamAge, streamBytes)
	}
	if _, err := js.Stream(ctx, "EVENTS"); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatalf("EVENTS must be gone, got %v", err)
	}
}

// TestEnsureRefusesToDestroyUndrainedData is the limit on that power. An empty stream
// is a name; a stream with messages is somebody's undrained hand-off whatever it is
// called. The ensure must fail LOUDLY and name it rather than silently deleting it —
// the report an operator can act on, which "subjects overlap with an existing stream"
// never was.
func TestEnsureRefusesToDestroyUndrainedData(t *testing.T) {
	_, cl := planeOn(t)
	ctx := context.Background()
	js := cl.JetStream()
	squat(t, js, "EVENTS")
	if _, err := js.Publish(ctx, "event.pageview", []byte(`{"org":"acme"}`)); err != nil {
		t.Fatalf("seed a message: %v", err)
	}

	err := EnsureEventStream(ctx, cl)
	if err == nil {
		t.Fatal("ensure must refuse to retire a stream that still holds messages")
	}
	for _, want := range []string{"EVENTS", "undrained", EventStream} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q so an operator can act on it; got: %v", want, err)
		}
	}

	// The data is still there.
	st, err := js.Stream(ctx, "EVENTS")
	if err != nil {
		t.Fatalf("EVENTS must survive: %v", err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("EVENTS holds %d messages, want its 1 kept", info.State.Msgs)
	}
}

// TestEnsureNeverRetiresATenantStream pins the blast radius. A tenant cannot reach
// these subjects today — the tenant endpoint roots every subject it accepts at pub.<org>.
// — so this state is unreachable, which is exactly why it is worth a test: if that
// rooting ever regressed, the platform must refuse rather than delete a customer's
// stream to make room for its own.
func TestEnsureNeverRetiresATenantStream(t *testing.T) {
	_, cl := planeOn(t)
	ctx := context.Background()
	js := cl.JetStream()
	tenant := pubsub.TenantPrefix + "acme-orders"
	squat(t, js, tenant)

	err := EnsureEventStream(ctx, cl)
	if err == nil {
		t.Fatal("ensure must refuse to retire a tenant stream")
	}
	if !strings.Contains(err.Error(), tenant) || !strings.Contains(err.Error(), "TENANT") {
		t.Fatalf("error must name the tenant stream and say what it is; got: %v", err)
	}
	if _, err := js.Stream(ctx, tenant); err != nil {
		t.Fatalf("the tenant's stream must survive untouched: %v", err)
	}
}

// TestPlaneReadyFollowsTheStream is the monitoring regression. /v1/event/health
// answered 200/ok on warehouse connectivity while 100% of writes 503'd, so the probe
// has to track the WRITE path — and track it live, not from a cached connection: the
// stream is precisely what went missing, and a client built once says nothing about
// whether it is still there.
func TestPlaneReadyFollowsTheStream(t *testing.T) {
	url, cl := planeOn(t)
	t.Setenv("CLOUD_PUBSUB_URL", url)
	conn.close()
	t.Cleanup(conn.close)

	ctx := context.Background()
	if err := planeReady(ctx); err != nil {
		t.Fatalf("plane must be ready once the ensure has run: %v", err)
	}

	// Delete the stream out from under the cached connection.
	if err := cl.JetStream().DeleteStream(ctx, EventStream); err != nil {
		t.Fatalf("delete the stream: %v", err)
	}
	if err := planeReady(ctx); err == nil {
		t.Fatal("probe reported ready with no stream — this is the blindness the outage hid behind")
	}

	// And it heals: the next probe re-dials, re-ensures, and is ready again.
	if err := planeReady(ctx); err != nil {
		t.Fatalf("probe must re-ensure the plane rather than latch failed: %v", err)
	}
}
