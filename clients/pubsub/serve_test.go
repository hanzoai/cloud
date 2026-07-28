package pubsub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsio "github.com/nats-io/nats.go"
)

// serve_test.go covers what the subsystem now PROMISES unconditionally.
//
// Mount is no longer gated, so two properties carry every cloud boot: the plane it
// serves must actually carry messages, and a plane that cannot bind must abort boot
// instead of leaving the process running with no messaging. A silent bind failure
// would be worse than the old opt-in, because nothing downstream would ever be told.

// A round trip through JetStream — not just "the port is open". Binding proves
// nothing about the plane: clients/webhooks (stream COMMERCE) and clients/kafka both
// depend on JetStream specifically, so the test publishes to a real stream and reads
// the payload back through an independent client.
func TestServesJetStreamRoundTrip(t *testing.T) {
	url := mountForTest(t)

	nc, err := natsio.Connect(url)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.AddStream(&natsio.StreamConfig{
		Name:     "ROUNDTRIP",
		Subjects: []string{"roundtrip.>"},
	}); err != nil {
		t.Fatalf("add stream (JetStream not usable): %v", err)
	}

	sub, err := js.SubscribeSync("roundtrip.one")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	want := []byte("payload-through-the-embedded-plane")
	if _, err := js.Publish("roundtrip.one", want); err != nil {
		t.Fatalf("publish: %v", err)
	}
	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("no message came back through the plane: %v", err)
	}
	if string(msg.Data) != string(want) {
		t.Fatalf("payload mismatch: got %q want %q", msg.Data, want)
	}
}

// NEGATIVE: the port is already taken. Mount MUST return an error so boot aborts.
//
// This is the property that makes always-on safe. If Open swallowed a bind failure,
// every consumer would dial a plane that was never there — the phantom messaging
// plane the fail-closed rule exists to prevent — and the process would look healthy.
func TestMountFailsClosedWhenPortIsTaken(t *testing.T) {
	// Hold a port for the duration so the embedded server cannot have it.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	_, port, err := net.SplitHostPort(occupied.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}

	srv = nil
	t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
	t.Setenv("CLOUD_PUBSUB_PORT", port)
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())

	err = Mount(testApp(), testDeps())
	if err == nil {
		t.Fatal("Mount returned nil on a taken port: boot would continue with NO messaging plane")
	}
	if srv != nil {
		t.Fatal("failed Mount left a server reference behind")
	}
}

// NEGATIVE: the store directory cannot be created. JetStream is file-backed, so an
// unusable store is an unusable plane and must also abort boot rather than degrade.
func TestMountFailsClosedWhenStoreDirUnusable(t *testing.T) {
	// A regular file where a directory must go — MkdirAll cannot succeed.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	srv = nil
	t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
	t.Setenv("CLOUD_PUBSUB_PORT", "-1")
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", filepath.Join(blocked, "store"))

	if err := Mount(testApp(), testDeps()); err == nil {
		t.Fatal("Mount returned nil with an unusable store dir: boot would continue with NO messaging plane")
	}
	if srv != nil {
		t.Fatal("failed Mount left a server reference behind")
	}
}

// The plane survives Shutdown/Mount cycles without leaking the previous server —
// a rolling restart re-mounts in-process.
func TestRemountAfterShutdown(t *testing.T) {
	first := mountForTest(t)
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	second := mountForTest(t)
	if second == "" {
		t.Fatal("remount produced no client URL")
	}
	if first == second {
		t.Fatalf("remount reused the old URL %q; the first server was not released", first)
	}
}

// mountForTest mounts the plane on a random free port over a temp store and returns
// its client URL, registering shutdown.
func mountForTest(t *testing.T) string {
	t.Helper()
	srv = nil
	t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
	t.Setenv("CLOUD_PUBSUB_PORT", "-1") // random free port
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())

	if err := Mount(testApp(), testDeps()); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if srv == nil {
		t.Fatal("Mount started no server")
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return srv.ClientURL()
}
