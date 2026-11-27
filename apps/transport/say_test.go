package transport

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// failing is a transport whose first `fail` sends return err and whose later
// ones are heard. It counts every attempt, heard or not.
func failing(h *heard, tries *atomic.Int32, fail int32, err error) transport {
	tr := listening(h, false)
	tr.send = func(_ context.Context, _ *cloud.Service[state], _ string, m Message) (Delivery, error) {
		if tries.Add(1) <= fail {
			return Delivery{}, err
		}
		h.add(m)
		return Delivery{}, nil
	}
	return tr
}

// quickly shortens the wait between offers so a test drives every try.
func quickly(t *testing.T) {
	t.Helper()
	prev := replyWait
	replyWait = time.Millisecond
	t.Cleanup(func() { replyWait = prev })
}

// A reply whose send fails is OFFERED AGAIN, and is said once it lands.
//
// This is the turn that ran, billed, and spoke into nothing: the run finished,
// one send to the transport timed out, the failure was logged, and the asker saw
// silence. A turn owes one reply and a failed send has not paid it.
func TestAReplyThatFailsToSendIsOfferedAgain(t *testing.T) {
	quickly(t)
	var h heard
	var tries atomic.Int32
	tr := failing(&h, &tries, 2, fmt.Errorf("zip: call chat_send: %w", context.DeadlineExceeded))

	say(context.Background(), service(), tr, "acme", asked(), "the deploy at 02:10 failed its probe")

	if got := tries.Load(); got != 3 {
		t.Fatalf("sends = %d, want 3 (two failures and the one that landed)", got)
	}
	if said := h.all(); len(said) != 1 || said[0].text != "the deploy at 02:10 failed its probe" {
		t.Fatalf("heard %+v, want the answer exactly once", said)
	}
}

// A transport that STAYS down is given up on after replyTries, so the turn
// releases its pool slot instead of holding it for the life of the outage.
func TestAReplyIsOfferedABoundedNumberOfTimes(t *testing.T) {
	quickly(t)
	var h heard
	var tries atomic.Int32
	tr := failing(&h, &tries, 1<<30, errors.New("zip: call chat_send: connection refused"))

	say(context.Background(), service(), tr, "acme", asked(), "answer")

	if got := tries.Load(); got != int32(replyTries) {
		t.Fatalf("sends = %d, want %d", got, replyTries)
	}
	if said := h.all(); len(said) != 0 {
		t.Fatalf("heard %+v from a transport that never accepted a send", said)
	}
}

// An app that is NOT DEPLOYED is not waited for. ErrNoPeer is the one failure
// that is a fact about the deployment rather than the moment, and no wait will
// deploy it — so it is tried once.
func TestAnAbsentTransportIsNotWaitedFor(t *testing.T) {
	quickly(t)
	var h heard
	var tries atomic.Int32
	tr := failing(&h, &tries, 1<<30, fmt.Errorf("%w: provider", plane.ErrNoPeer))

	say(context.Background(), service(), tr, "acme", asked(), "answer")

	if got := tries.Load(); got != 1 {
		t.Fatalf("sends = %d, want 1", got)
	}
}

// A turn whose context has ended stops offering: what is left to protect is the
// goroutine, and a reply no one is waiting on does not hold it.
func TestAReplyStopsWhenTheTurnEnds(t *testing.T) {
	prev := replyWait
	replyWait = time.Hour
	t.Cleanup(func() { replyWait = prev })

	var h heard
	var tries atomic.Int32
	tr := failing(&h, &tries, 1<<30, errors.New("zip: call chat_send: connection refused"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { say(ctx, service(), tr, "acme", asked(), "answer"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("say kept waiting after its turn ended")
	}
	if got := tries.Load(); got != 1 {
		t.Fatalf("sends = %d, want 1", got)
	}
}
