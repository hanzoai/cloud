package writerpin

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSingleWriterHeldImmediatelyAndNeverLost(t *testing.T) {
	p := NewSingleWriter()
	if p.Kind() != "single-writer" {
		t.Fatalf("kind=%q", p.Kind())
	}
	h, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	select {
	case <-h.Lost():
		t.Fatal("single-writer pin reported Lost before Release")
	case <-time.After(20 * time.Millisecond):
		// expected: never lost
	}
	h.Release()
	select {
	case <-h.Lost():
		// expected: Lost fires after Release
	case <-time.After(time.Second):
		t.Fatal("Lost did not fire after Release")
	}
	h.Release() // idempotent, must not panic
}

func TestSingleWriterRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSingleWriter().Acquire(ctx); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestConsensusPinFailsClosed(t *testing.T) {
	p := NewConsensusPin()
	h, err := p.Acquire(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
	if h != nil {
		t.Fatal("ConsensusPin must not hand out a Held it cannot back")
	}
}

func TestResolveDefaultsToSingleWriter(t *testing.T) {
	if Resolve().Kind() != "single-writer" {
		t.Fatalf("Resolve should default to single-writer until consensus is wired")
	}
}
