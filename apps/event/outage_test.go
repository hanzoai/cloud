package event

// outage_test.go — a store that is down must hold facts on the bus, not spend them.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// TestAStoreOutageLosesNoFact is the regression. The sink answered every failed insert
// with an immediate Nak, so while the store restarted a fact was redelivered maxDeliver
// times within milliseconds, abandoned, and logged as ACKNOWLEDGED DATA LOST — on a
// routine restart, for facts the endpoint had already answered 200 for.
//
// It runs the real sink against a real embedded bus, holds the store down for far longer
// than the old budget lasted, and then brings it back: every fact must land, and none
// may be abandoned.
func TestAStoreOutageLosesNoFact(t *testing.T) {
	url, cl := planeOn(t)
	t.Setenv("CLOUD_PUBSUB_URL", url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := EnsureEventStream(ctx, cl); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	var up atomic.Bool
	var mu sync.Mutex
	landed := map[string]bool{}
	ready, exec := warehouseReady, warehouseExec
	t.Cleanup(func() { warehouseReady, warehouseExec = ready, exec })
	warehouseReady = up.Load
	warehouseExec = func(_ context.Context, _ string, args ...any) error {
		if !up.Load() {
			return errors.New("dial tcp datastore:9000: connection refused")
		}
		mu.Lock()
		landed[fmt.Sprint(args...)] = true
		mu.Unlock()
		return nil
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(landed)
	}

	const facts = 20
	for i := range facts {
		m := message{Signal: string(signalError), Org: "acme", Time: time.Now().UTC(), ID: fmt.Sprintf("f%d", i), Name: "boom"}
		if _, err := cl.PublishJSONToStream(ctx, signalError.subject(), m); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	abandoned := lostExhausted.Load()
	s := &drain{log: luxlog.New("test")}
	go func() { _ = s.consume(ctx, cl) }()

	// The old budget ran out in milliseconds; this outage lasts seconds.
	time.Sleep(2 * time.Second)
	if n := count(); n != 0 {
		t.Fatalf("%d facts landed while the store was down", n)
	}
	if got := lostExhausted.Load(); got != abandoned {
		t.Fatalf("%d facts abandoned during a %s outage — the store being down spent their deliveries",
			got-abandoned, 2*time.Second)
	}

	up.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for count() < facts && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := count(); n != facts {
		t.Fatalf("%d of %d facts landed once the store came back", n, facts)
	}
	if got := lostExhausted.Load(); got != abandoned {
		t.Fatalf("%d facts abandoned", got-abandoned)
	}
}

// TestTheRetryBudgetOutlastsAnOutageAndEnds pins the schedule: the first retry is quick,
// no wait exceeds retryMax, the deliveries span more than half an hour of outage, and
// they are finite, so a fact the store rejects on every attempt still stops.
func TestTheRetryBudgetOutlastsAnOutageAndEnds(t *testing.T) {
	if got := retryDelay(1); got != retryFirst {
		t.Errorf("first retry after %s, want %s", got, retryFirst)
	}
	var total time.Duration
	for n := uint64(1); n < maxDeliver; n++ {
		d := retryDelay(n)
		if d < retryFirst || d > retryMax {
			t.Fatalf("retry %d waits %s, outside [%s, %s]", n, d, retryFirst, retryMax)
		}
		if n > 1 && d < retryDelay(n-1) {
			t.Fatalf("retry %d waits %s, less than retry %d", n, d, n-1)
		}
		total += d
	}
	if total < 30*time.Minute {
		t.Errorf("%d deliveries span %s; a store down that long loses facts", maxDeliver, total)
	}
	if total > 2*time.Hour {
		t.Errorf("%d deliveries span %s; a poisonous fact retries for too long", maxDeliver, total)
	}
}
