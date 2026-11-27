package amqp

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/amqp/protocol"
	psembed "github.com/hanzoai/pubsub/embed"
	natsio "github.com/nats-io/nats.go"
	amqp091 "github.com/rabbitmq/amqp091-go"
)

// TestEmbeddedAMQPInteroperatesWithEmbeddedJetStream proves the two embeds work
// together in one process, driven by the reference RabbitMQ client with nothing
// mocked: an embedded PubSub (NATS+JetStream) plus an embedded AMQP gateway that
// dials it, and github.com/rabbitmq/amqp091-go declaring a queue, publishing to
// it, consuming it and acking — the whole path a real client walks.
//
// The last two assertions are the crux of the fold. A plain NATS subscriber sees
// the body on amqp.<exchange>.<routing key>, so the translation is real and not
// a private format; and the queue's JetStream consumer shows nothing
// outstanding, so the client's basic.ack reached the bus rather than being
// swallowed by a gateway that had already acked on its behalf.
func TestEmbeddedAMQPInteroperatesWithEmbeddedJetStream(t *testing.T) {
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

	b := protocol.NewBroker(protocol.Config{
		Addr:      "127.0.0.1:0", // random free AMQP port
		PubSubURL: url,
	})
	errc := make(chan error, 1)
	go func() { errc <- b.Serve() }()
	defer b.Shutdown()

	// Serve connects, declares the stream and the topology bucket, and binds
	// the listener BEFORE Ready closes; an early return is a startup failure.
	select {
	case err := <-errc:
		t.Fatalf("gateway Serve returned before it was ready: %v", err)
	case <-b.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("gateway never became ready")
	}

	conn, err := amqp091.Dial("amqp://" + b.Addr().String())
	if err != nil {
		t.Fatalf("amqp client dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	if err := ch.ExchangeDeclare("orders", "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("exchange.declare: %v", err)
	}
	if _, err := ch.QueueDeclare("shipping", true, false, false, false, nil); err != nil {
		t.Fatalf("queue.declare: %v", err)
	}
	if err := ch.QueueBind("shipping", "order.#", "orders", false, nil); err != nil {
		t.Fatalf("queue.bind: %v", err)
	}

	// A NATS-native subscriber on the subject the mapping names. It has never
	// heard of AMQP and needs nothing from this package to find the traffic.
	nc, err := natsio.Connect(url)
	if err != nil {
		t.Fatalf("verify client connect: %v", err)
	}
	defer nc.Close()
	watch, err := nc.SubscribeSync("amqp.orders.order.created")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	deliveries, err := ch.Consume("shipping", "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("basic.consume: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body := []byte(`{"order":"A-1"}`)
	if err := ch.PublishWithContext(ctx, "orders", "order.created", false, false, amqp091.Publishing{
		ContentType:   "application/json",
		MessageId:     "m-1",
		CorrelationId: "c-1",
		Headers:       amqp091.Table{"attempt": int32(1)},
		Body:          body,
	}); err != nil {
		t.Fatalf("basic.publish: %v", err)
	}

	var got amqp091.Delivery
	select {
	case m, ok := <-deliveries:
		if !ok {
			t.Fatal("delivery channel closed before a message arrived")
		}
		got = m
	case <-time.After(20 * time.Second):
		t.Fatal("the AMQP client consumed nothing within 20s")
	}
	if string(got.Body) != string(body) {
		t.Fatalf("body = %q, want %q", got.Body, body)
	}
	if got.Exchange != "orders" || got.RoutingKey != "order.created" {
		t.Fatalf("delivered as %q/%q, want orders/order.created", got.Exchange, got.RoutingKey)
	}
	if got.ContentType != "application/json" || got.MessageId != "m-1" || got.CorrelationId != "c-1" {
		t.Fatalf("properties did not survive: %+v", got)
	}
	if got.Headers["attempt"] != int32(1) {
		t.Fatalf("header attempt = %#v, want int32(1)", got.Headers["attempt"])
	}
	if err := got.Ack(false); err != nil {
		t.Fatalf("basic.ack: %v", err)
	}

	msg, err := watch.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("a plain NATS subscriber saw nothing on amqp.orders.order.created: %v", err)
	}
	if string(msg.Data) != string(body) {
		t.Fatalf("bus body = %q, want %q", msg.Data, body)
	}

	// The ack has to reach JetStream, or "acked" means nothing.
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream ctx: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := js.ConsumerInfo(protocol.Stream, "shipping")
		if err != nil {
			t.Fatalf("the embedded AMQP gateway did not create a JetStream consumer for the queue: %v", err)
		}
		if info.NumAckPending == 0 && info.NumPending == 0 && info.Delivered.Consumer >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the client acked: %d pending, %d awaiting ack, %d delivered — the AMQP ack did not reach the embedded JetStream",
				info.NumPending, info.NumAckPending, info.Delivered.Consumer)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
