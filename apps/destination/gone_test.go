package destination

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/commerce/infra"
	"github.com/hanzoai/pubsub-go/jetstream"
	psembed "github.com/hanzoai/pubsub/embed"
)

// A pull that reaches the bus after the durable is gone is answered "no
// responders", not "consumer deleted". The subscription has to end on that answer
// too, or the loop never makes the durable again and the app reads nothing.
func TestASubscriptionEndsWhenItsDurableIsGone(t *testing.T) {
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	defer srv.Shutdown()
	ctx := context.Background()
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: srv.ClientURL(), Name: "test", EnableJetStream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateConsumer(ctx, event.EventStream, &infra.ConsumerConfig{Name: durable, Durable: durable, AckPolicy: infra.AckExplicit}); err != nil {
		t.Fatal(err)
	}
	cons, err := cl.JetStream().Consumer(ctx, event.EventStream, durable)
	if err != nil {
		t.Fatal(err)
	}
	// Gone before the first pull is sent: the order a busy process can see between
	// binding the durable and consuming it.
	if err := cl.JetStream().DeleteConsumer(ctx, event.EventStream, durable); err != nil {
		t.Fatal(err)
	}
	cc, err := cons.Consume(func(jetstream.Msg) {}, jetstream.ConsumeErrHandler(gone))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Stop()
	// The client's own next look is a missed heartbeat, 30s out, and it answers that
	// by pulling again: without gone this never closes.
	select {
	case <-cc.Closed():
	case <-time.After(10 * time.Second):
		t.Fatal("the subscription outlived its durable")
	}
}
