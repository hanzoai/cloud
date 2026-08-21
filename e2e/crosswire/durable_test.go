package crosswire

import (
	"context"
	"fmt"
	"testing"
	"time"

	amqpproto "github.com/hanzoai/amqp/protocol"
	psembed "github.com/hanzoai/pubsub/embed"
	natsio "github.com/nats-io/nats.go"
	amqp091 "github.com/rabbitmq/amqp091-go"
)

// restart is one embedded bus + AMQP gateway over a fixed store, with a way to
// take it down and bring it back on the SAME store — a real crash/recover.
type restart struct {
	store string
	ps    *psembed.Server
	ab    *amqpproto.Broker
}

func openBus(t *testing.T, store string) *restart {
	t.Helper()
	ps, err := psembed.Open(psembed.Options{Host: "127.0.0.1", Port: -1, ServerName: "durable", StoreDir: store})
	if err != nil {
		t.Fatalf("pubsub open (%s): %v", store, err)
	}
	ab := amqpproto.NewBroker(amqpproto.Config{Addr: "127.0.0.1:0", PubSubURL: ps.ClientURL()})
	go ab.Serve()
	select {
	case <-ab.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("amqp never ready")
	}
	return &restart{store: store, ps: ps, ab: ab}
}

func (r *restart) addr() string { return r.ab.Addr().String() }
func (r *restart) url() string  { return r.ps.ClientURL() }

func (r *restart) crash() {
	r.ab.Shutdown()
	r.ps.Shutdown()
	time.Sleep(700 * time.Millisecond) // let the OS flush and release the ports
}

// TestDurableUndeliveredSurviveBusRestart: messages published to a durable
// queue and never consumed are still there after the WHOLE bus is taken down
// and brought back on the same on-disk store. This is the core durability claim
// through the AMQP wire.
func TestDurableUndeliveredSurviveBusRestart(t *testing.T) {
	store := t.TempDir()
	r := openBus(t, store)

	conn, err := amqp091.Dial("amqp://" + r.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := ch.QueueDeclare("work", true, false, false, false, nil); err != nil {
		t.Fatalf("declare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("m-%d", i)
		want[id] = true
		if err := ch.PublishWithContext(ctx, "", "work", false, false, amqp091.Publishing{
			MessageId: id, Body: []byte("payload-" + id),
		}); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	conn.Close()
	time.Sleep(300 * time.Millisecond)

	r.crash()
	r = openBus(t, store)
	defer r.crash()

	conn2, err := amqp091.Dial("amqp://" + r.addr())
	if err != nil {
		t.Fatalf("dial #2: %v", err)
	}
	defer conn2.Close()
	ch2, err := conn2.Channel()
	if err != nil {
		t.Fatalf("channel #2: %v", err)
	}
	// The durable queue must already exist across the restart.
	if _, err := ch2.QueueDeclarePassive("work", true, false, false, false, nil); err != nil {
		t.Fatalf("queue 'work' did not survive the restart (passive declare failed): %v", err)
	}
	del, err := ch2.Consume("work", "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume #2: %v", err)
	}
	got := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(got) < len(want) {
		select {
		case d := <-del:
			got[d.MessageId] = true
			_ = d.Ack(false)
		case <-deadline:
			t.Fatalf("after restart recovered %d/%d messages: %v", len(got), len(want), keys(got))
		}
	}
	t.Logf("PROVEN durable through AMQP: all %d messages published before a full bus crash were recovered after it, from the same store. %v", len(got), keys(got))
}

// TestDurableAcksAreDurable: a message acked before the crash does NOT come back
// after it. Settlement is confirmed on the bus (NumAckPending==0, NumPending==0)
// BEFORE the crash, so a redelivery afterwards would be a real durability
// violation, not a race.
func TestDurableAcksAreDurable(t *testing.T) {
	store := t.TempDir()
	r := openBus(t, store)

	conn, err := amqp091.Dial("amqp://" + r.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := ch.QueueDeclare("settled", true, false, false, false, nil); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := ch.Qos(1, 0, false); err != nil {
		t.Fatalf("qos: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const n = 3
	for i := 0; i < n; i++ {
		if err := ch.PublishWithContext(ctx, "", "settled", false, false, amqp091.Publishing{
			MessageId: fmt.Sprintf("s-%d", i), Body: []byte("x"),
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	del, err := ch.Consume("settled", "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	for i := 0; i < n; i++ {
		select {
		case d := <-del:
			if err := d.Ack(false); err != nil {
				t.Fatalf("ack: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("received only %d of %d", i, n)
		}
	}
	// Confirm on the bus that nothing is outstanding BEFORE the crash.
	waitSettled(t, r.url(), "settled")
	conn.Close()

	r.crash()
	r = openBus(t, store)
	defer r.crash()

	// Confirm again after the restart, and additionally try to consume: nothing
	// should be redelivered.
	waitSettled(t, r.url(), "settled")
	conn2, err := amqp091.Dial("amqp://" + r.addr())
	if err != nil {
		t.Fatalf("dial #2: %v", err)
	}
	defer conn2.Close()
	ch2, err := conn2.Channel()
	if err != nil {
		t.Fatalf("channel #2: %v", err)
	}
	del2, err := ch2.Consume("settled", "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume #2: %v", err)
	}
	select {
	case d := <-del2:
		t.Fatalf("message %s came back after being acked and surviving a crash — ack was not durable", d.MessageId)
	case <-time.After(4 * time.Second):
	}
	t.Logf("PROVEN acks are durable through AMQP: %d messages acked before a full bus crash stayed settled after it; none redelivered.", n)
}

func waitSettled(t *testing.T, url, queue string) {
	t.Helper()
	nc, err := natsio.Connect(url)
	if err != nil {
		t.Fatalf("settled connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("settled js: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		ci, err := js.ConsumerInfo(amqpproto.Stream, queue)
		if err == nil && ci.NumPending == 0 && ci.NumAckPending == 0 {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("consumer %q info: %v", queue, err)
			}
			t.Fatalf("consumer %q never settled: NumPending=%d NumAckPending=%d", queue, ci.NumPending, ci.NumAckPending)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
