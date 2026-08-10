package risk

// shutdown_test.go — a rollout writes every tenant's model down, whatever the
// background is doing.
//
// THE DEFECT. close() used to be `p.stop(); p.wg.Wait()` and only THEN save every
// resident. The wait was unbounded and the saves were queued behind it, so a
// rollout that caught background work in flight lost EVERY tenant's model:
//
//   - the process gets a 30-second shutdown window (serve.go), and the pod's
//     grace period is 60s;
//   - a search finishing after cancellation still calls its meter and then writes
//     its result to the tenant's shelf, whose durable Sync is bounded at
//     durableOpTimeout — 30 SECONDS, the whole window, on its own;
//   - so wg.Wait() outlives the window, the process is killed, and not one
//     resident model was written down.
//
// A model that came back empty refuses to score, and a refusal reads as CLEAN to
// anything that does not check it — so this is a control switching itself off for
// the length of a warm period, fleet-wide, once per deploy.
//
// The ordering is the fix: the saves are what must not be lost, so they run
// UNCONDITIONALLY and never behind the drain. Background work gets whatever is
// left of the caller's window, and a drain that did not finish is a NAMED state
// (ErrDrainIncomplete) rather than a silence.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRollout_WritesEveryModelDownEvenWithBackgroundWorkStuck is the blocker.
// Two tenants have learned; background work is in flight and will NOT finish
// inside the window. Both models must survive the restart anyway.
func TestRollout_WritesEveryModelDownEvenWithBackgroundWorkStuck(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)

	first := planeAt(t, dir)
	teach(t, first, a, stream(300, time.Now().UTC().Add(-5*time.Hour)))
	teach(t, first, b, stream(200, time.Now().UTC().Add(-4*time.Hour)))
	beforeA, _, _ := first.state(a)
	beforeB, _, _ := first.state(b)
	if beforeA.Learned == 0 || beforeB.Learned == 0 {
		t.Fatal("neither model learned anything — the test would prove nothing")
	}

	// Background work in flight that outlives the shutdown window. This is a
	// search's shape as close() sees it: something registered on the plane's
	// waitgroup that has not returned. Held open until after close() returns, so
	// the wait CANNOT be satisfied within the window.
	stuck := make(chan struct{})
	first.wg.Go(func() {
		<-stuck
	})

	// The window the composition root actually hands a teardown hook.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	began := time.Now()
	err := first.close(ctx)
	took := time.Since(began)
	close(stuck)

	// The drain did not finish, and close SAYS SO rather than hanging or lying.
	if !errors.Is(err, ErrDrainIncomplete) {
		t.Fatalf("close with stuck background work must report ErrDrainIncomplete, got %v", err)
	}
	// It returned inside the window instead of blocking until SIGKILL.
	if took > 10*time.Second {
		t.Fatalf("close blocked %v on background work — a rollout would be SIGKILLed before it saved anything", took)
	}

	// THE PROPERTY: both tenants' models are on disk, despite the stuck drain.
	second := planeAt(t, dir)
	defer func() { _ = second.close(context.Background()) }()
	afterA, _, err := second.state(a)
	if err != nil {
		t.Fatalf("state A after rollout: %v", err)
	}
	afterB, _, err := second.state(b)
	if err != nil {
		t.Fatalf("state B after rollout: %v", err)
	}
	if afterA.Learned != beforeA.Learned {
		t.Fatalf("tenant A came back having learned %d of %d — a rollout with background work in flight returned it to warming",
			afterA.Learned, beforeA.Learned)
	}
	if afterB.Learned != beforeB.Learned {
		t.Fatalf("tenant B came back having learned %d of %d — one tenant's stuck background work cost ANOTHER tenant its model",
			afterB.Learned, beforeB.Learned)
	}
}

// TestRollout_CleanDrainIsNotReportedAsIncomplete keeps the named state honest:
// it must fire on a drain that did NOT finish and stay quiet on one that did.
// Without this, "always report incomplete" would pass the test above.
func TestRollout_CleanDrainIsNotReportedAsIncomplete(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	p := planeAt(t, dir)
	teach(t, p, k, stream(120, time.Now().UTC().Add(-3*time.Hour)))

	if err := p.close(context.Background()); err != nil {
		t.Fatalf("a clean shutdown must report no error at all, got %v", err)
	}
}

// TestRollout_HonoursTheCallersWindow proves the bound is the CALLER's and not a
// constant of our own: an already-expired window must not be waited out.
func TestRollout_HonoursTheCallersWindow(t *testing.T) {
	probe.reset(true)
	p := planeAt(t, t.TempDir())
	stuck := make(chan struct{})
	p.wg.Go(func() {
		<-stuck
	})

	// A window that is already over. close must return about immediately — the
	// drain budget is a CEILING, not a floor to sit out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	err := p.close(ctx)
	took := time.Since(began)
	close(stuck)

	if !errors.Is(err, ErrDrainIncomplete) {
		t.Fatalf("an expired window with work in flight is an incomplete drain, got %v", err)
	}
	if took > drainBudget {
		t.Fatalf("close waited %v on an already-expired context — it ignored the caller's window and used its own budget (%v)", took, drainBudget)
	}
}
