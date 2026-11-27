package ledger

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	psembed "github.com/hanzoai/pubsub/embed"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/driver/pubsub/stream"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/meter"
)

// onBus starts an embedded plane and a ledger with $1 in acme's wallet.
func onBus(t *testing.T) (*meter.Client, money.FinanceClient) {
	t.Helper()
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	t.Setenv("CLOUD_PUBSUB_URL", srv.ClientURL())
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	bus.Disconnect()
	ensured.Store(false)
	t.Cleanup(func() { bus.Disconnect(); ensured.Store(false); srv.Shutdown() })

	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	if _, err := fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100)}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	c, err := meter.New(meter.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return c, fin
}

func spent(t *testing.T, fin money.FinanceClient) money.Amount {
	t.Helper()
	bal, err := fin.Balance(context.Background(), "acme", "acme", "usd", false)
	if err != nil {
		t.Fatal(err)
	}
	return money.FromCents(100).Sub(bal)
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func pending(t *testing.T) uint64 {
	t.Helper()
	js, _, err := stream.Bus()
	if err != nil {
		t.Fatal(err)
	}
	s, err := js.Stream(context.Background(), usageStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

// unacked is a delivered message whose ack never happens, as when the drain dies
// after the ledger committed its batch and before it acked.
type unacked struct{ jetstream.Msg }

func (unacked) Ack() error { return nil }

// N posts are N debits, exactly: none while the ledger has not drained them, all of
// them after, none twice when a drain dies between the ship and the ack and the batch
// is delivered again, and none for a message replayed past the stream's dedup window.
func TestEveryPostedDebitLandsOnce(t *testing.T) {
	c, fin := onBus(t)
	ctx := context.Background()
	const n = 40
	each, _ := money.ParseUSD("0.000000777")
	var refs []string
	for i := 0; i < n; i++ {
		if err := c.Post(ctx, meter.Usage{User: "acme", Org: "acme", Amount: each.Unwrap(), Model: "kai"}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	if got := pending(t); got != n {
		t.Fatalf("stream holds %d debits, want %d", got, n)
	}
	if s := spent(t, fin); !s.IsZero() {
		t.Fatalf("the ledger moved %s before any drain", s)
	}

	// A drain that dies after the ledger has the batch and before it acks.
	js, _, _ := stream.Bus()
	cons, err := js.CreateOrUpdateConsumer(ctx, usageStream, jetstream.ConsumerConfig{
		Durable: ledgerDurable, AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 4 * drainBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(n, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var got, lost []jetstream.Msg
	for m := range batch.Messages() {
		got = append(got, m)
		lost = append(lost, unacked{m})
		var d debit
		if err := json.Unmarshal(m.Data(), &d); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, d.Usage.Ref)
	}
	if len(got) != n {
		t.Fatalf("fetched %d, want %d", len(got), n)
	}
	(&books{}).post(ctx, lost, t.Logf)
	want := money.Zero()
	for i := 0; i < n; i++ {
		want = want.Add(each)
	}
	if s := spent(t, fin); s.Cmp(want) != 0 {
		t.Fatalf("after the batch the ledger moved %s, want %s", s, want)
	}
	for _, m := range got {
		_ = m.Nak()
	}

	// The batch is delivered again: every debit is a replay the ledger already holds.
	dctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { c.Drain(dctx, t.Logf); close(done) }()
	until(t, "the stream to drain", func() bool { return pending(t) == 0 })
	if s := spent(t, fin); s.Cmp(want) != 0 {
		t.Fatalf("after the redelivery the ledger moved %s, want %s", s, want)
	}

	// A debit replayed past the dedup window, under a new message id.
	if _, err := js.Publish(ctx, usageSubject, got[0].Data(), jetstream.WithMsgID("replay-"+refs[0])); err != nil {
		t.Fatal(err)
	}
	until(t, "the replay to drain", func() bool { return pending(t) == 0 })
	if s := spent(t, fin); s.Cmp(want) != 0 {
		t.Fatalf("after a replay the ledger moved %s, want %s", s, want)
	}

	// New posts after all that land once each.
	for i := 0; i < 3; i++ {
		if err := c.Post(ctx, meter.Usage{User: "acme", Org: "acme", Amount: each.Unwrap(), Model: "kai"}); err != nil {
			t.Fatal(err)
		}
		want = want.Add(each)
	}
	until(t, "three more debits", func() bool { return spent(t, fin).Cmp(want) == 0 })
	stop()
	<-done
}

// A post the stream cannot take is written to the ledger directly, so a bus outage
// costs the caller the ledger's wait and never the debit.
func TestAPostTheBusCannotTakeIsRecorded(t *testing.T) {
	c, fin := onBus(t)
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	bus.Disconnect()
	ensured.Store(false)
	each, _ := money.ParseUSD("0.25")
	if err := c.Post(context.Background(), meter.Usage{User: "acme", Org: "acme", Amount: each.Unwrap()}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if s := spent(t, fin); s.Cmp(each) != 0 {
		t.Fatalf("the ledger moved %s, want %s", s, each)
	}
}

// A debit a cloud process did not sign moves no money: the bus answers anyone who
// can reach its port.
func TestAnUnsignedDebitIsNotPosted(t *testing.T) {
	c, fin := onBus(t)
	ctx := context.Background()
	each, _ := money.ParseUSD("0.5")
	if err := c.Post(ctx, meter.Usage{User: "acme", Org: "acme", Amount: each.Unwrap()}); err != nil {
		t.Fatal(err)
	}
	js, _, _ := stream.Bus()
	cons, err := js.CreateOrUpdateConsumer(ctx, usageStream, jetstream.ConsumerConfig{
		Durable: ledgerDurable, AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 4 * drainBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for m := range batch.Messages() {
		data = m.Data()
		_ = m.Ack()
	}
	for _, sig := range []string{"", "00"} {
		forged := &nats.Msg{Subject: usageSubject, Data: data, Header: nats.Header{}}
		if sig != "" {
			forged.Header.Set(sigHeader, sig)
		}
		if _, err := js.PublishMsg(ctx, forged, jetstream.WithMsgID("forged-"+sig)); err != nil {
			t.Fatal(err)
		}
	}
	dctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { c.Drain(dctx, t.Logf); close(done) }()
	until(t, "the forged debits to be set aside", func() bool { return pending(t) == 0 })
	stop()
	<-done
	if s := spent(t, fin); !s.IsZero() {
		t.Fatalf("an unsigned debit moved %s", s)
	}
}
