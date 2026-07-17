package sync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// engine_test.go proves the KIND-AGNOSTIC engine with a fake provider: the loop
// guard (an event by the sync's own actor is skipped), cursor idempotency
// (identical fingerprints are a no-op), direction off, chained propagation, and the
// hop limit that terminates a chain. No git, no network — the engine in isolation.

// fakeProvider is a Provider double: it records every Reconcile and reports a change
// (so the engine advances the cursor and fires the chain). The engine drives it
// synchronously (cloud.Sync → runOne → propagate are one goroutine), so no lock.
type fakeProvider struct {
	kind       string
	reconciled []Event
}

func (f *fakeProvider) Kind() string { return f.kind }

func (f *fakeProvider) Reconcile(_ context.Context, sy Sync, ev Event) (bool, error) {
	f.reconciled = append(f.reconciled, ev)
	return true, nil
}

func (f *fakeProvider) count() int { return len(f.reconciled) }

func mountSync(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

func testStore(t *testing.T) *store {
	t.Helper()
	st, err := storeFor(mounted.Load(), "acme")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	return st
}

// seed inserts a sync and returns it (read back so the id is the stored one).
func seed(t *testing.T, st *store, l Sync) Sync {
	t.Helper()
	l.Org = "acme"
	if l.CreatedAt == 0 {
		l.CreatedAt = time.Now().Unix()
	}
	l.UpdatedAt = l.CreatedAt
	if err := st.Upsert(context.Background(), l); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	got, err := st.GetByEndpoints(context.Background(), "acme", l.Kind, l.Source.Locator, l.Target.Locator)
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}
	return got
}

func TestEngineReconcileAndCursorIdempotency(t *testing.T) {
	mountSync(t)
	fp := &fakeProvider{kind: "fake"}
	registerProvider(fp)
	st := testStore(t)
	seed(t, st, Sync{
		ID: "s1", Kind: "fake", Direction: dirBoth, Actor: "bot",
		Source: Endpoint{Provider: "src", Locator: "s1"},
		Target: Endpoint{Provider: "tgt", Locator: "t1"},
	})

	ev := cloud.SyncEvent{Kind: "fake", Provider: "src", Org: "acme", Locator: "s1", Branch: "main", After: "aaa", Actor: "human"}
	if r, _ := cloud.Sync(context.Background(), ev); r.Ran != 1 {
		t.Fatalf("first event want Ran=1, got %+v", r)
	}
	// Same fingerprint again → idempotent no-op.
	if r, _ := cloud.Sync(context.Background(), ev); r.Ran != 0 || r.Skipped != 1 {
		t.Fatalf("identical event want Ran=0 Skipped=1, got %+v", r)
	}
	// A new tip reconciles again.
	ev.After = "bbb"
	if r, _ := cloud.Sync(context.Background(), ev); r.Ran != 1 {
		t.Fatalf("new tip want Ran=1, got %+v", r)
	}
	if fp.count() != 2 {
		t.Fatalf("provider must have reconciled twice, got %d", fp.count())
	}
}

func TestEngineLoopGuardAndDirection(t *testing.T) {
	mountSync(t)
	fp := &fakeProvider{kind: "fake"}
	registerProvider(fp)
	st := testStore(t)
	seed(t, st, Sync{
		ID: "s1", Kind: "fake", Direction: dirBoth, Actor: "bot",
		Source: Endpoint{Provider: "src", Locator: "s1"},
		Target: Endpoint{Provider: "tgt", Locator: "t1"},
	})

	// An event by the sync's OWN actor is the echo → skipped.
	loop := cloud.SyncEvent{Kind: "fake", Provider: "src", Org: "acme", Branch: "main", After: "aaa", Actor: "bot"}
	if r, _ := cloud.Sync(context.Background(), loop); r.Skipped != 1 || r.Ran != 0 {
		t.Fatalf("loop event want Skipped=1, got %+v", r)
	}
	// A manual run bypasses the loop guard even for the same actor.
	manual := loop
	manual.Manual = true
	if r, _ := cloud.Sync(context.Background(), manual); r.Ran != 1 {
		t.Fatalf("manual run want Ran=1, got %+v", r)
	}
	if fp.count() != 1 {
		t.Fatalf("only the manual run should reconcile, got %d", fp.count())
	}

	// direction off → never reconciles.
	seed(t, st, Sync{
		ID: "s2", Kind: "fake", Direction: dirOff, Actor: "bot",
		Source: Endpoint{Provider: "src2", Locator: "s2"},
		Target: Endpoint{Provider: "tgt", Locator: "t2"},
	})
	off := cloud.SyncEvent{Kind: "fake", Provider: "src2", Org: "acme", Branch: "main", After: "zzz", Actor: "human"}
	if r, _ := cloud.Sync(context.Background(), off); r.Ran != 0 {
		t.Fatalf("direction off want Ran=0, got %+v", r)
	}
}

func TestEngineChainPropagation(t *testing.T) {
	mountSync(t)
	fp := &fakeProvider{kind: "fake"}
	registerProvider(fp)
	st := testStore(t)
	// A: s1 → m1 ; B: m1 → d1. An event for A propagates to B (B's source is m1).
	seed(t, st, Sync{
		ID: "A", Kind: "fake", Direction: dirBoth, Actor: "botA",
		Source: Endpoint{Provider: "head", Locator: "s1"},
		Target: Endpoint{Provider: "mid", Locator: "m1"},
	})
	seed(t, st, Sync{
		ID: "B", Kind: "fake", Direction: dirBoth, Actor: "botB",
		Source: Endpoint{Provider: "mid", Locator: "m1"},
		Target: Endpoint{Provider: "dst", Locator: "d1"},
	})

	ev := cloud.SyncEvent{Kind: "fake", Provider: "head", Org: "acme", Locator: "s1", Repo: "r", Branch: "main", After: "aaa", Actor: "human"}
	if r, _ := cloud.Sync(context.Background(), ev); r.Ran != 1 {
		t.Fatalf("A want Ran=1 (B reconciles via chain, not the top count), got %+v", r)
	}
	// Both reconciled: A directly, B via propagation.
	if fp.count() != 2 {
		t.Fatalf("chain must reconcile A then B (2), got %d", fp.count())
	}
}

func TestEngineHopLimitTerminatesChain(t *testing.T) {
	mountSync(t)
	fp := &fakeProvider{kind: "fake"}
	registerProvider(fp)
	st := testStore(t)
	// A chain of maxHops+3 links: head "s" → n0 → n1 → ... Only the head is resolved
	// by the event's provider; each hop is found by the next locator. The chain is
	// LONGER than the hop limit, so it must be cut at maxHops+1 reconciles.
	seed(t, st, Sync{
		ID: "L0", Kind: "fake", Direction: dirBoth, Actor: "a0",
		Source: Endpoint{Provider: "head", Locator: "s"},
		Target: Endpoint{Provider: "link", Locator: "n0"},
	})
	for i := 1; i <= maxHops+2; i++ {
		seed(t, st, Sync{
			ID: fmt.Sprintf("L%d", i), Kind: "fake", Direction: dirBoth, Actor: fmt.Sprintf("a%d", i),
			Source: Endpoint{Provider: "chain", Locator: fmt.Sprintf("n%d", i-1)},
			Target: Endpoint{Provider: "link", Locator: fmt.Sprintf("n%d", i)},
		})
	}

	ev := cloud.SyncEvent{Kind: "fake", Provider: "head", Org: "acme", Locator: "s", Repo: "r", Branch: "main", After: "aaa", Actor: "human"}
	cloud.Sync(context.Background(), ev)
	if got := fp.count(); got != maxHops+1 {
		t.Fatalf("chain must terminate at maxHops+1=%d reconciles, got %d", maxHops+1, got)
	}
}
