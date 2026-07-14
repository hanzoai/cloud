package cloud

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestEmitLifecycleFanOut proves the seam's contract: one event reaches every
// registered subscriber, each on a cancel-immune context, a panicking subscriber
// is contained (does not crash the process or starve the others), and a
// no-subscriber emit is a silent no-op.
func TestEmitLifecycleFanOut(t *testing.T) {
	ResetLifecycleSubscribers()
	t.Cleanup(ResetLifecycleSubscribers)

	var mu sync.Mutex
	got := map[string]LifecycleEvent{}
	ctxErr := map[string]error{}
	done := make(chan string, 8)

	for _, name := range []string{"a", "b"} {
		name := name
		RegisterLifecycleSubscriber(func(ctx context.Context, ev LifecycleEvent) {
			mu.Lock()
			got[name] = ev
			ctxErr[name] = ctx.Err()
			mu.Unlock()
			done <- name
		})
	}
	// A panicking subscriber must be contained.
	RegisterLifecycleSubscriber(func(_ context.Context, _ LifecycleEvent) { panic("boom") })

	// A cancelled parent ctx must NOT cancel the subscriber's ctx — the fact already
	// happened; the notify/mirror runs regardless.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	EmitLifecycle(ctx, LifecycleEvent{Kind: LifecyclePushLanded, Org: "acme", Repo: "code", Branch: "main", After: "deadbeef"})

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a subscriber did not run within 2s")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"a", "b"} {
		ev, ok := got[name]
		if !ok {
			t.Fatalf("subscriber %s never received the event", name)
		}
		if ev.Org != "acme" || ev.Repo != "code" || ev.Branch != "main" || ev.After != "deadbeef" {
			t.Fatalf("subscriber %s got wrong event: %+v", name, ev)
		}
		if ctxErr[name] != nil {
			t.Fatalf("subscriber %s ran on a cancelled ctx (%v) — EmitLifecycle must use WithoutCancel", name, ctxErr[name])
		}
	}
}

func TestEmitLifecycleNoSubscribers(t *testing.T) {
	ResetLifecycleSubscribers()
	// No panic, no goroutine, just returns.
	EmitLifecycle(context.Background(), LifecycleEvent{Kind: LifecycleDeployLive, Org: "x"})
}

// TestRegisterLifecycleNilIgnored proves a nil registration is a no-op (defensive:
// a mis-wired subsystem can never install a nil that panics on emit).
func TestRegisterLifecycleNilIgnored(t *testing.T) {
	ResetLifecycleSubscribers()
	t.Cleanup(ResetLifecycleSubscribers)
	RegisterLifecycleSubscriber(nil)
	EmitLifecycle(context.Background(), LifecycleEvent{Kind: LifecyclePushLanded})
}
