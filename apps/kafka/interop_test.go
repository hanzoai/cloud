package kafka

import (
	"testing"
	"time"

	psembed "github.com/hanzoai/pubsub/embed"
	"github.com/hanzoai/kafka/protocol"
	"github.com/hanzoai/kafka/types"
	natsio "github.com/nats-io/nats.go"
)

// TestEmbeddedKafkaInteroperatesWithEmbeddedJetStream proves the two embeds work
// together in one process: an embedded PubSub (NATS+JetStream) plus an embedded
// Kafka adaptor that dials it. When the broker starts it calls EnsureOffsetBucket,
// which creates the JetStream KV bucket "kafka-consumer-offsets" ON the embedded
// server. Asserting that bucket exists (read back with an independent client)
// proves the Kafka wire adaptor is translating to/from the embedded JetStream —
// the crux of the fold.
func TestEmbeddedKafkaInteroperatesWithEmbeddedJetStream(t *testing.T) {
	ps, err := psembed.Open(psembed.Options{
		Host:       "127.0.0.1",
		Port:       -1, // random free port
		ServerName: "interop",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("pubsub open: %v", err)
	}
	defer ps.Shutdown()
	url := ps.ClientURL()

	b := protocol.NewBroker(&types.Configuration{
		PubSubUrl:      url,
		BrokerPort:     0, // random free Kafka port (unused: interop verified via JetStream)
		AdminPort:      0, // admin HTTP disabled
		NodeID:         1,
		StreamReplicas: 1,
		StorageType:    "file",
	})
	errc := make(chan error, 1)
	go func() { errc <- b.Serve() }()
	defer b.Shutdown()

	// Serve connects + EnsureOffsetBucket BEFORE it listens; an early return is a
	// startup failure.
	select {
	case err := <-errc:
		t.Fatalf("broker Serve returned early: %v", err)
	case <-time.After(3 * time.Second):
	}

	nc, err := natsio.Connect(url)
	if err != nil {
		t.Fatalf("verify client connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream ctx: %v", err)
	}
	if _, err := js.KeyValue("kafka-consumer-offsets"); err != nil {
		t.Fatalf("embedded Kafka broker did not create its offset bucket on the embedded JetStream: %v", err)
	}
}
