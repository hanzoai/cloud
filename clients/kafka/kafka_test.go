package kafka

import (
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testDeps() cloud.Deps { return cloud.Deps{Logger: luxlog.New("test")} }
func testApp() *zip.App    { return zip.New(zip.Config{Logger: luxlog.New("test")}) }

// TestMountDisabledIsNoop: with CLOUD_KAFKA_ENABLED unset, Mount binds nothing.
func TestMountDisabledIsNoop(t *testing.T) {
	broker = nil
	if err := Mount(testApp(), testDeps()); err != nil {
		t.Fatalf("disabled Mount: %v", err)
	}
	if broker != nil {
		t.Fatal("disabled Mount started a broker")
	}
}

// TestMountEnabledFailsClosedWhenPubSubUnreachable: enabled with an unreachable
// PubSub, Mount returns an error (never a phantom broker) — the fail-closed,
// no-silent-half-embed guarantee.
func TestMountEnabledFailsClosedWhenPubSubUnreachable(t *testing.T) {
	broker = nil
	t.Setenv("CLOUD_KAFKA_ENABLED", "true")
	t.Setenv("CLOUD_KAFKA_PUBSUB_URL", "nats://127.0.0.1:1") // connection refused, fast
	t.Setenv("CLOUD_KAFKA_PORT", "0")
	if err := Mount(testApp(), testDeps()); err == nil {
		t.Fatal("enabled Mount with unreachable pubsub must fail closed")
	}
	if broker != nil {
		t.Fatal("failed Mount must not leave a broker ref")
	}
}
