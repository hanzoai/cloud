package event

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/commerce/infra"
	"github.com/hanzoai/pubsub-go/jetstream"
)

// TestAConsumerSharedByTwoReplicasHandlesEachMessageOnce is the property the shared
// plane exists for. Two pods are two clients of ONE server, each binding the same
// durable on the event stream; every message is handled by exactly one of them, and
// both get work. With an embedded server per pod each pod would see only its own
// events and handle all of them, and an event published on one pod's server would
// never reach the other's consumer.
func TestAConsumerSharedByTwoReplicasHandlesEachMessageOnce(t *testing.T) {
	url, a := planeOn(t)
	b, err := infra.NewPubSubClient(context.Background(), &infra.PubSubConfig{URL: url, Name: "second-pod", EnableJetStream: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()
	if err := EnsureEventStream(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := EnsureEventStream(ctx, b); err != nil { // idempotent: the second pod to boot
		t.Fatal(err)
	}
	for _, cl := range []*infra.PubSubClient{a, b} {
		if _, err := cl.CreateConsumer(ctx, EventStream, &infra.ConsumerConfig{
			Name: "shared", Durable: "shared", DeliverPolicy: infra.DeliverNew, AckPolicy: infra.AckExplicit,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	byPod := map[string]int{}
	var total atomic.Int32
	for name, cl := range map[string]*infra.PubSubClient{"a": a, "b": b} {
		cons, err := cl.JetStream().Consumer(ctx, EventStream, "shared")
		if err != nil {
			t.Fatal(err)
		}
		cc, err := cons.Consume(func(m jetstream.Msg) {
			time.Sleep(2 * time.Millisecond) // work, so one pod cannot take everything
			mu.Lock()
			seen[string(m.Data())]++
			byPod[name]++
			mu.Unlock()
			_ = m.Ack()
			total.Add(1)
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cc.Stop)
	}
	const n = 200
	for i := range n {
		if _, err := a.JetStream().Publish(ctx, "event.page", []byte(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for total.Load() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // a duplicate would arrive by now
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("%d of %d messages handled", len(seen), n)
	}
	for m, c := range seen {
		if c != 1 {
			t.Fatalf("%s handled %d times", m, c)
		}
	}
	if byPod["a"] == 0 || byPod["b"] == 0 {
		t.Fatalf("work was not shared: %v", byPod)
	}
}

// The plane states the copies the deployment keeps: one on an embedded node, the
// cluster's three where CLOUD_PUBSUB_REPLICAS says so.
func TestTheStreamStatesTheDeploymentsReplicas(t *testing.T) {
	if got := withReplicas(eventStream).Replicas; got != 1 {
		t.Fatalf("default replicas %d, want 1", got)
	}
	t.Setenv("CLOUD_PUBSUB_REPLICAS", "3")
	if got := withReplicas(eventStream).Replicas; got != 3 {
		t.Fatalf("replicas %d, want 3", got)
	}
	t.Setenv("CLOUD_PUBSUB_REPLICAS", "nonsense")
	if got := withReplicas(eventStream).Replicas; got != 1 {
		t.Fatalf("a bad value is %d, want 1", got)
	}
}
