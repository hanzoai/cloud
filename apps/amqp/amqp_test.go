package amqp

import (
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testDeps() cloud.Deps { return cloud.Deps{} }
func testApp() *zip.App    { return zip.New(zip.Config{Logger: luxlog.New("test")}) }

// TestMountFailsClosedWhenPubSubUnreachable: with an unreachable PubSub, Mount
// returns an error and leaves nothing behind — never a phantom gateway holding
// :5672 open with no bus under it. That is the fail-closed, no-silent-half-embed
// guarantee, and it is the whole reason Mount waits on Ready rather than
// assuming.
func TestMountFailsClosedWhenPubSubUnreachable(t *testing.T) {
	gateway = nil
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1") // the ONE bus knob; refused, fast
	t.Setenv("CLOUD_AMQP_PORT", "0")
	if err := Use(testApp(), testDeps()); err == nil {
		t.Fatal("Mount with an unreachable pubsub must fail closed")
	}
	if gateway != nil {
		t.Fatal("a failed Mount must not leave a gateway ref")
	}
}

// TestShutdownIsIdempotent: cloud calls Shutdown on every app, including ones
// whose Mount failed or never ran.
func TestShutdownIsIdempotent(t *testing.T) {
	gateway = nil
	for range 2 {
		if err := Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown with no gateway: %v", err)
		}
	}
}

// TestBadPortIsRefused: a port that is not a number is a configuration error,
// answered before anything binds.
func TestBadPortIsRefused(t *testing.T) {
	gateway = nil
	t.Setenv("CLOUD_AMQP_PORT", "five thousand")
	err := Use(testApp(), testDeps())
	if err == nil {
		t.Fatal("a non-numeric CLOUD_AMQP_PORT must be refused")
	}
	if gateway != nil {
		t.Fatal("a failed Mount must not leave a gateway ref")
	}
}
