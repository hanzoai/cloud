package coord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// A deployment of several writers whose shared KV cannot be asked grants no lease: each pod
// counts only its own share of one, which is none, so none of them can be sure it is alone.
func TestALeaseIsRefusedWhenSeveralPodsCannotReachTheStore(t *testing.T) {
	for _, declared := range []int{2, 3} {
		c := coordtest.Down(coord.Config{Declared: declared})
		if _, _, err := c.Lease("test", "seal", time.Minute)(context.Background()); !errors.Is(err, coord.ErrHeld) {
			t.Fatalf("%d declared pods, store down: %v, want ErrHeld", declared, err)
		}
	}
}

// A deployment of one writer needs no store to be alone: the lease is granted from this
// process's own count, which is exact because nothing else writes. That is right for one
// writer and would be wrong for two, which is what the test above pins.
func TestALeaseIsGrantedLocallyToTheOnlyWriterWhenTheStoreIsDown(t *testing.T) {
	c := coordtest.Down(coord.Config{Declared: 1})
	lease := c.Lease("test", "seal", time.Minute)
	give, _, err := lease(context.Background())
	if err != nil {
		t.Fatalf("one declared pod, store down: %v, want a lease", err)
	}
	if _, _, err := lease(context.Background()); !errors.Is(err, coord.ErrHeld) {
		t.Fatalf("a second taker in the same process: %v, want ErrHeld", err)
	}
	give()
}

// The lease says how long the holder has it left, so the holder can bound its work inside
// it: never more than the ttl, and some of it.
func TestALeaseSaysHowLongItIsHeld(t *testing.T) {
	_, held, err := coord.Alone().Lease("test", "ttl", 3*time.Second)(context.Background())
	if err != nil || held <= 0 || held > 3*time.Second {
		t.Fatalf("held %v, err %v; want some of 3s", held, err)
	}
}

// late is a store that answers a count only when the test says: the lease it grants was
// asked for before the answer came.
type late struct {
	coord.Backend // only Quota is asked
	answer        chan struct{}
}

func (l late) Quota(_ context.Context, keys []string, _ []int, _ []time.Duration) ([]int, []time.Duration, bool, error) {
	<-l.answer
	return make([]int, len(keys)), nil, true, nil
}

// A lease counts from before it was asked for, so a slow store takes its delay out of what
// the holder has. The test sleeps a known delay before the store answers, and a monotonic
// clock cannot show less than that, so the bound does not depend on runner speed.
func TestALeaseSaysWhatIsLeftAfterTheStoreWasSlow(t *testing.T) {
	const ttl, delay = time.Minute, 50 * time.Millisecond
	l := late{answer: make(chan struct{})}
	go func() { time.Sleep(delay); close(l.answer) }()
	_, held, err := coord.OnBackend(l, coord.Config{Declared: 1, Secret: coordtest.Secret}).Lease("test", "slow", ttl)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if held > ttl-delay {
		t.Fatalf("held %v after a store that took at least %v; want at most %v", held, delay, ttl-delay)
	}
}

// A store that answers after the whole ttl has gone grants nothing: the lease is over
// before the holder has it.
func TestALeaseTheStoreAnsweredTooLateForIsRefused(t *testing.T) {
	const ttl, delay = 50 * time.Millisecond, 60 * time.Millisecond
	l := late{answer: make(chan struct{})}
	go func() { time.Sleep(delay); close(l.answer) }()
	give, held, err := coord.OnBackend(l, coord.Config{Declared: 1, Secret: coordtest.Secret}).Lease("test", "slow", ttl)(context.Background())
	if !errors.Is(err, coord.ErrHeld) || give != nil || held != 0 {
		t.Fatalf("got held %v, err %v; want ErrHeld and nothing to give back", held, err)
	}
}
