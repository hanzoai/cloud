package coord

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A lease is held by one taker at a time, per name and key, and free again once given back.
func TestALeaseIsHeldByOneAtATime(t *testing.T) {
	c := Alone()
	ctx := context.Background()
	lease := c.Lease("test", "seal", time.Minute)
	give, _, err := lease(ctx)
	if err != nil {
		t.Fatalf("the first taker: %v", err)
	}
	if _, _, err := lease(ctx); !errors.Is(err, ErrHeld) {
		t.Fatalf("a second taker: %v, want ErrHeld", err)
	}
	if _, _, err := c.Lease("test", "other", time.Minute)(ctx); err != nil {
		t.Fatalf("another key is another lease: %v", err)
	}
	if _, _, err := c.Lease("elsewhere", "seal", time.Minute)(ctx); err != nil {
		t.Fatalf("another name is another lease: %v", err)
	}
	give()
	if _, _, err := lease(ctx); err != nil {
		t.Fatalf("after it was given back: %v", err)
	}
}

// A release made after the lease has lapsed gives back nothing: by then the lease may be
// another taker's, and lowering the shared count would free it for a third. Nothing here
// races a timer: A's lease is short and B's is a minute, B takes as soon as A's has lapsed
// (waiting longer only makes that surer), and B's minute is far longer than the test.
func TestAReleaseAfterTheLeaseLapsedGivesBackNothing(t *testing.T) {
	c := Alone()
	ctx := context.Background()
	short, long := c.Lease("test", "late", 30*time.Millisecond), c.Lease("test", "late", time.Minute)
	giveA, _, err := short(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var giveB func()
	for end := time.Now().Add(10 * time.Second); giveB == nil; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("A's lease never lapsed")
		}
		if g, _, err := long(ctx); err == nil {
			giveB = g
		} else if !errors.Is(err, ErrHeld) {
			t.Fatal(err)
		}
	}
	giveA() // A is slow, and lets go of what is no longer its own
	if _, _, err := long(ctx); !errors.Is(err, ErrHeld) {
		t.Fatalf("A's late release freed B's lease: a third taker got %v, want ErrHeld", err)
	}
	giveB()
	if _, _, err := long(ctx); err != nil {
		t.Fatalf("B's own release: %v", err)
	}
}
