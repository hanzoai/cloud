package email

// Email runs on every replica. These tests stand a second replica beside the first: a
// second Service beside the same database, which is what another pod is; on SQLite there
// is one process and the Service's Keyed is the whole lock.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// replicaOf is another replica's delivery service beside w's.
func (w *world) replicaOf() *Service {
	if locus.DSN() == "" {
		return w.svc
	}
	return &Service{resolver: w.svc.resolver, sendFunc: w.svc.sendFunc, log: w.svc.log, now: w.svc.now}
}

// Two events that each say a message was delivered (a provider's retry under another id,
// a second provider's report), filed together on two replicas, count it once: each
// replica reads the message to see what it has already reached, so without the org's
// filing lock across replicas both find it undelivered and both count the delivery. Each
// filing waits, once it has read the message, for the other to have read it, which is the
// moment two replicas would each count it.
func TestADeliveryReportedToTwoReplicasIsCountedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t)
		second := w.replicaOf()

		var mu sync.Mutex
		var writes int
		met := make(chan struct{})
		was := upsertFn
		upsertFn = func(ctx context.Context, in *client.BaseUpsertIn) (*client.BaseUpserted, error) {
			if in.Collection == messages {
				mu.Lock()
				writes++
				first := writes <= 2
				if writes == 2 {
					close(met)
				}
				mu.Unlock()
				if first {
					select {
					case <-met:
					case <-time.After(300 * time.Millisecond): // alone: the lock held the other out
					}
				}
			}
			return was(ctx, in)
		}
		t.Cleanup(func() { upsertFn = was })

		events := []*Event{
			{ID: "ses:delivered", Provider: "ses", Kind: KindDelivered, PID: "ses-1", Message: worldMsg, At: signedAt, To: []string{"bounced@acme.io"}},
			{ID: "ses:delivered-again", Provider: "ses", Kind: KindDelivered, PID: "ses-1", Message: worldMsg, At: signedAt, To: []string{"bounced@acme.io"}},
		}
		var wg sync.WaitGroup
		for i, svc := range []*Service{w.svc, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := svc.Record(context.Background(), "acme", []*Event{events[i]}); err != nil {
					t.Errorf("record %s: %v", events[i].ID, err)
				}
			}()
		}
		wg.Wait()
		if s := w.tally(t); s.Totals.Delivered != 1 {
			t.Fatalf("the day counts %d deliveries of one message, want 1", s.Totals.Delivered)
		}
		if got, err := w.svc.GetMessage(context.Background(), "acme", w.msg.ID); err != nil || got.Status != StatusDelivered {
			t.Fatalf("the message reads %+v (%v), want delivered", got, err)
		}
	})
}
