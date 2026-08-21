// Package crosswire is the adversarial cross-wire E2E for the message plane:
// ONE embedded bus (NATS+JetStream), an AMQP gateway and a Kafka adaptor both
// riding it, driven by the reference wire clients (rabbitmq/amqp091-go,
// twmb/franz-go, nats.go). It exists to answer ONE question the interop suites
// do not: is a message published through one wire readable through the OTHER
// wire? The central architecture claim says yes ("one bus, so a message
// published through any wire is readable through the others"). These tests
// determine whether that is true, false, or true-only-for-a-native-reader.
package crosswire

import (
	"context"
	"testing"
	"time"

	amqpproto "github.com/hanzoai/amqp/protocol"
	kafkaproto "github.com/hanzoai/kafka/protocol"
	kafkapubsub "github.com/hanzoai/kafka/pubsub"
	kafkatypes "github.com/hanzoai/kafka/types"
	psembed "github.com/hanzoai/pubsub/embed"
	natsio "github.com/nats-io/nats.go"
	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/twmb/franz-go/pkg/kgo"
)

type plane struct {
	url      string
	amqpAddr string
	kafkaSeed string
}

// bring stands up one embedded bus with both wire adaptors on it. Everything is
// real: the NATS server, the JetStream store, the AMQP listener, the Kafka
// listener. No mocks.
func bring(t *testing.T) plane {
	t.Helper()
	ps, err := psembed.Open(psembed.Options{
		Host: "127.0.0.1", Port: -1, ServerName: "crosswire", StoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("pubsub open: %v", err)
	}
	t.Cleanup(ps.Shutdown)
	url := ps.ClientURL()

	ab := amqpproto.NewBroker(amqpproto.Config{Addr: "127.0.0.1:0", PubSubURL: url})
	aerr := make(chan error, 1)
	go func() { aerr <- ab.Serve() }()
	t.Cleanup(ab.Shutdown)
	select {
	case err := <-aerr:
		t.Fatalf("amqp Serve returned early: %v", err)
	case <-ab.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("amqp never ready")
	}

	kb := kafkaproto.NewBroker(&kafkatypes.Configuration{
		PubSubUrl: url, BrokerHost: "127.0.0.1", BrokerPort: 0, AdminPort: 0,
		NodeID: 1, StreamReplicas: 1, StorageType: "file",
	})
	kerr := make(chan error, 1)
	go func() { kerr <- kb.Serve() }()
	t.Cleanup(kb.Shutdown)
	select {
	case err := <-kerr:
		t.Fatalf("kafka Serve returned early: %v", err)
	case <-time.After(3 * time.Second):
	}

	return plane{url: url, amqpAddr: ab.Addr().String(), kafkaSeed: kb.Addr().String()}
}

// publishAMQP declares an exchange+queue+binding and publishes one message
// through the AMQP wire. Returns nothing; the point is the side effect on the bus.
func publishAMQP(t *testing.T, p plane, exchange, key string, body []byte) {
	t.Helper()
	conn, err := amqp091.Dial("amqp://" + p.amqpAddr)
	if err != nil {
		t.Fatalf("amqp dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("amqp channel: %v", err)
	}
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("exchange.declare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ch.PublishWithContext(ctx, exchange, key, false, false, amqp091.Publishing{
		ContentType: "application/json", Body: body,
	}); err != nil {
		t.Fatalf("basic.publish: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The four cross-wire probes.
// ---------------------------------------------------------------------------

// A message published through AMQP is visible to a plain NATS subscriber on the
// subject the AMQP mapping names. This is the TRUE half of "one bus": a
// bus-native reader sees the AMQP subtree. (This is also what the amqp interop
// suite asserts; reproduced here so the four probes read side by side.)
func TestAMQP_to_NativeNATS(t *testing.T) {
	p := bring(t)
	nc, err := natsio.Connect(p.url)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("amqp.orders.order.created")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	nc.Flush()

	body := []byte(`{"order":"A-1"}`)
	publishAMQP(t, p, "orders", "order.created", body)

	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("native NATS saw nothing on amqp.orders.order.created: %v", err)
	}
	if string(msg.Data) != string(body) {
		t.Fatalf("native NATS body = %q, want %q", msg.Data, body)
	}
	t.Logf("PROVEN: AMQP publish is readable by a native NATS subscriber (raw body, subject amqp.orders.order.created)")
}

// A message published through Kafka is visible to a plain NATS subscriber on the
// kafka subject — but the DATA is a Kafka RecordBatch, not the logical value.
func TestKafka_to_NativeNATS(t *testing.T) {
	p := bring(t)

	// Native subscriber on the kafka partition subject BEFORE producing.
	nc, err := natsio.Connect(p.url)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("kafka-events-0.data")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	nc.Flush()

	value := []byte("kafka-value-1")
	produceKafka(t, p, "events", value)

	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("native NATS saw nothing on kafka-events-0.data: %v", err)
	}
	// The bus carries the message, but as a Kafka RecordBatch frame. The logical
	// value is buried inside it; it is NOT msg.Data verbatim.
	if string(msg.Data) == string(value) {
		t.Fatalf("unexpected: kafka stored the raw value; it is supposed to store a RecordBatch frame")
	}
	if !contains(msg.Data, value) {
		t.Fatalf("native NATS bus payload does not even contain the value bytes; framing is opaque")
	}
	t.Logf("PROVEN: Kafka publish reaches the bus on kafka-events-0.data, but as a RecordBatch (len=%d) that WRAPS the value — a native subscriber must Kafka-decode it", len(msg.Data))
}

// THE CENTRAL CLAIM, part 1: a message published through AMQP, read through the
// KAFKA wire. If the plane is truly one shared namespace, a Kafka consumer of
// some topic would see it. It does not — different stream, different subject,
// different framing.
func TestAMQP_to_Kafka_CrossWire(t *testing.T) {
	p := bring(t)

	// Give a Kafka consumer every chance: pre-create the topic whose name would
	// have to line up for the claim to hold, and subscribe from the start.
	createKafkaTopic(t, p, "orders")

	body := []byte(`{"order":"CROSSWIRE"}`)
	publishAMQP(t, p, "orders", "order.created", body)

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(p.kafkaSeed),
		kgo.ConsumeTopics("orders"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("kafka client: %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	fetches := cl.PollFetches(ctx)
	got := 0
	fetches.EachRecord(func(r *kgo.Record) { got++ })

	if got != 0 {
		t.Fatalf("CLAIM HELD? a Kafka consumer read %d records an AMQP client published — investigate", got)
	}
	t.Logf("DISPROVEN cross-wire AMQP->Kafka: an AMQP publish (subject amqp.orders.order.created, stream AMQP) is INVISIBLE to a Kafka consumer of topic 'orders' (stream kafka-orders-0, subject kafka-orders-0.data). Separate namespaces on one bus.")
}

// THE CENTRAL CLAIM, part 2: a message published through Kafka, read through the
// AMQP wire. An AMQP queue bound to catch it sees nothing — the AMQP consumer
// only ever reads stream AMQP under amqp.>, and Kafka writes stream
// kafka-orders-0 under kafka-orders-0.data.
func TestKafka_to_AMQP_CrossWire(t *testing.T) {
	p := bring(t)

	// An AMQP queue bound as broadly as the wire allows: fanout wildcard on the
	// default exchange plus a topic '#' — the most catch-all an AMQP client can
	// express. None of it reaches a kafka-* subject.
	conn, err := amqp091.Dial("amqp://" + p.amqpAddr)
	if err != nil {
		t.Fatalf("amqp dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("amqp channel: %v", err)
	}
	if err := ch.ExchangeDeclare("everything", "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("exchange.declare: %v", err)
	}
	if _, err := ch.QueueDeclare("catchall", true, false, false, false, nil); err != nil {
		t.Fatalf("queue.declare: %v", err)
	}
	if err := ch.QueueBind("catchall", "#", "everything", false, nil); err != nil {
		t.Fatalf("queue.bind: %v", err)
	}
	deliveries, err := ch.Consume("catchall", "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("basic.consume: %v", err)
	}

	produceKafka(t, p, "events", []byte("kafka-value-CROSSWIRE"))

	select {
	case d := <-deliveries:
		t.Fatalf("CLAIM HELD? an AMQP consumer received %q a Kafka client produced — investigate", d.Body)
	case <-time.After(4 * time.Second):
	}
	t.Logf("DISPROVEN cross-wire Kafka->AMQP: a Kafka publish (subject kafka-events-0.data) is INVISIBLE to an AMQP queue bound '#' (reads stream AMQP, subject amqp.>). Separate namespaces on one bus.")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func produceKafka(t *testing.T, p plane, topic string, value []byte) {
	t.Helper()
	createKafkaTopic(t, p, topic)
	cl, err := kgo.NewClient(kgo.SeedBrokers(p.kafkaSeed), kgo.DefaultProduceTopic(topic))
	if err != nil {
		t.Fatalf("kafka producer client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Value: value}).FirstErr(); err != nil {
		t.Fatalf("kafka produce: %v", err)
	}
}

// createKafkaTopic creates the partition stream(s) for a topic DIRECTLY on the
// bus, the same streams the broker's own CreateTopic handler makes. Doing it
// through the lib removes the CreateTopics wire path as a variable: the probe is
// about read visibility, not topic administration.
func createKafkaTopic(t *testing.T, p plane, topic string) {
	t.Helper()
	c, err := kafkapubsub.NewClient(p.url)
	if err != nil {
		t.Fatalf("kafka pubsub client: %v", err)
	}
	defer c.Close()
	if c.TopicExists(topic) {
		return
	}
	if err := c.CreateTopicStreams(topic, 1, 1, natsio.FileStorage); err != nil {
		t.Fatalf("create topic streams %q: %v", topic, err)
	}
}

func contains(hay, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
