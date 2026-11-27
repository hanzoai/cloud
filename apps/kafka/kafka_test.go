package kafka

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testDeps() cloud.Deps { return cloud.Deps{} }
func testApp() *zip.App    { return zip.New(zip.Config{Logger: luxlog.New("test")}) }

// TestMountEnabledFailsClosedWhenPubSubUnreachable: enabled with an unreachable
// PubSub, Mount returns an error (never a phantom broker) — the fail-closed,
// no-silent-half-embed guarantee.
func TestMountFailsClosedWhenPubSubUnreachable(t *testing.T) {
	broker = nil
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1") // the ONE bus knob; refused, fast
	t.Setenv("CLOUD_KAFKA_PORT", "0")
	if err := Use(testApp(), testDeps()); err == nil {
		t.Fatal("enabled Mount with unreachable pubsub must fail closed")
	}
	if broker != nil {
		t.Fatal("failed Mount must not leave a broker ref")
	}
}

// TestMountRefusesAnUnreadableRetention: a retention that does not parse fails
// Mount, naming the variable, instead of quietly taking the default.
func TestMountRefusesAnUnreadableRetention(t *testing.T) {
	for key, val := range map[string]string{
		"CLOUD_KAFKA_RETENTION":       "a week",
		"CLOUD_KAFKA_RETENTION_BYTES": "1GiB",
	} {
		t.Run(key, func(t *testing.T) {
			broker = nil
			t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
			t.Setenv("CLOUD_KAFKA_PORT", "0")
			t.Setenv(key, val)
			err := Use(testApp(), testDeps())
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Use with %s=%q: err %v; want one naming %s", key, val, err, key)
			}
			if broker != nil {
				t.Fatal("failed Mount must not leave a broker ref")
			}
		})
	}
}
