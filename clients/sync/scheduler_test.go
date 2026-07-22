package sync

import (
	"context"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// scheduler_test.go proves the freshness driver: env-gating (fail-safe disable), the
// poll-only gate, filesystem org enumeration, the by-path discovery read, a full sweep
// that reconciles EVERY org's poll syncs (and only those) through the shared runOne
// core, and the ticker→sweep→drain lifecycle. No network — a counting provider stands
// in for git, so the sweep's routing is what's under test.

// countingProvider is a thread-safe Provider double (the sweep reconciles concurrently,
// unlike the engine's synchronous webhook path, so unlike engine_test's fakeProvider it
// must lock). It records every reconcile and reports a change so runOne advances.
type countingProvider struct {
	kind   string
	mu     sync.Mutex
	events []Event
}

func (f *countingProvider) Kind() string { return f.kind }

func (f *countingProvider) Reconcile(_ context.Context, _ Sync, ev Event) (bool, error) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	return true, nil
}

func (f *countingProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *countingProvider) allManual() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if !e.Manual {
			return false
		}
	}
	return true
}

// seedOrg upserts a sync into an arbitrary org's store (engine_test's seed is acme-only).
func seedOrg(t *testing.T, org string, l Sync) Sync {
	t.Helper()
	st, err := storeFor(mounted.Load(), org)
	if err != nil {
		t.Fatalf("storeFor %s: %v", org, err)
	}
	l.Org = org
	if l.CreatedAt == 0 {
		l.CreatedAt = time.Now().Unix()
	}
	l.UpdatedAt = l.CreatedAt
	if err := st.Upsert(context.Background(), l); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	got, err := st.GetByEndpoints(context.Background(), org, l.Kind, l.Source.Locator, l.Target.Locator)
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}
	return got
}

func TestReconcileIntervalEnv(t *testing.T) {
	log := luxlog.New("test")
	cases := map[string]time.Duration{
		"":      0,
		"0":     0,
		"off":   0,
		"OFF":   0,
		"false": 0,
		"nope":  0, // unparseable → disabled (fail-safe), never a surprise cadence
		"-5m":   0, // non-positive → disabled
		"10m":   10 * time.Minute,
		"30s":   30 * time.Second,
	}
	for raw, want := range cases {
		t.Setenv(reconcileIntervalEnv, raw)
		if got := reconcileInterval(log); got != want {
			t.Fatalf("reconcileInterval(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestPollable(t *testing.T) {
	git := func(dir, trig string) Sync {
		return Sync{Kind: "git", Direction: dir, Trigger: trig}
	}
	if !pollable(git(dirPull, trigPoll)) {
		t.Fatal("pull+poll git must be pollable")
	}
	if !pollable(git(dirBoth, trigPoll)) {
		t.Fatal("both+poll git must be pollable")
	}
	if pollable(git(dirPull, trigWebhook)) {
		t.Fatal("webhook trigger must NOT be swept")
	}
	if pollable(git(dirPull, trigManual)) {
		t.Fatal("manual trigger must NOT be swept")
	}
	if pollable(git(dirOff, trigPoll)) {
		t.Fatal("direction off must NOT be swept")
	}
	if pollable(Sync{Kind: "storage", Direction: dirPull, Trigger: trigPoll}) {
		t.Fatal("non-git kind must NOT be swept (git is the only provider today)")
	}
}

func TestSweepReconcilesOnlyPollSyncsAcrossOrgs(t *testing.T) {
	mountSync(t)
	fp := &countingProvider{kind: "git"}
	registerProvider(fp) // override the real git provider for this sweep

	old := time.Now().Unix() - 30 // so a reconcile's updated_at bump is visibly greater
	// acme: one poll (swept) + a webhook, a manual, and an off (all skipped).
	pollSync := seedOrg(t, "acme", Sync{
		ID: "p", Kind: "git", Direction: dirPull, Trigger: trigPoll, CreatedAt: old,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/cloud.git"},
		Target: Endpoint{Provider: provNative, Locator: "cloud"},
	})
	seedOrg(t, "acme", Sync{
		ID: "w", Kind: "git", Direction: dirPull, Trigger: trigWebhook, CreatedAt: old,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/ai.git"},
		Target: Endpoint{Provider: provNative, Locator: "ai"},
	})
	seedOrg(t, "acme", Sync{
		ID: "m", Kind: "git", Direction: dirBoth, Trigger: trigManual, CreatedAt: old,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/world.git"},
		Target: Endpoint{Provider: provNative, Locator: "world"},
	})
	seedOrg(t, "acme", Sync{
		ID: "o", Kind: "git", Direction: dirOff, Trigger: trigPoll, CreatedAt: old,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/zen.git"},
		Target: Endpoint{Provider: provNative, Locator: "zen"},
	})
	// beta: one poll (swept) — proves multi-org enumeration.
	seedOrg(t, "beta", Sync{
		ID: "bp", Kind: "git", Direction: dirBoth, Trigger: trigPoll, CreatedAt: old,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/universe.git"},
		Target: Endpoint{Provider: provNative, Locator: "universe"},
	})

	sweep(mounted.Load(), context.Background()) // synchronous: returns after all reconciles

	if fp.count() != 2 {
		t.Fatalf("sweep must reconcile exactly the 2 poll syncs (acme/p + beta/bp), got %d", fp.count())
	}
	if !fp.allManual() {
		t.Fatal("every scheduled reconcile must be Manual (the fetch is the change detector)")
	}
	// The swept sync's updated_at advanced (last-synced time); the skipped ones did not.
	got, err := storeForTest(t, "acme").Get(context.Background(), "acme", pollSync.ID)
	if err != nil {
		t.Fatalf("re-read poll sync: %v", err)
	}
	if got.UpdatedAt <= old {
		t.Fatalf("poll sync updated_at must advance past %d, got %d", old, got.UpdatedAt)
	}
	skipped, _ := storeForTest(t, "acme").Get(context.Background(), "acme", "w")
	if skipped.UpdatedAt != old {
		t.Fatalf("webhook sync must NOT be touched by the sweep, updated_at=%d want %d", skipped.UpdatedAt, old)
	}
}

func storeForTest(t *testing.T, org string) *store {
	t.Helper()
	st, err := storeFor(mounted.Load(), org)
	if err != nil {
		t.Fatalf("storeFor %s: %v", org, err)
	}
	return st
}

func TestSchedulerLifecycle(t *testing.T) {
	// Disabled ⇒ a no-op stop that is safe to call (idempotently).
	t.Setenv(reconcileIntervalEnv, "off")
	mountSync(t)
	stop := startScheduler(mounted.Load())
	stop()
	stop() // idempotent

	// Enabled at a tight cadence ⇒ the ticker fires a sweep; stop drains cleanly.
	t.Setenv(reconcileIntervalEnv, "20ms")
	fp := &countingProvider{kind: "git"}
	registerProvider(fp)
	seedOrg(t, "acme", Sync{
		ID: "p", Kind: "git", Direction: dirPull, Trigger: trigPoll,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/hanzoai/cloud.git"},
		Target: Endpoint{Provider: provNative, Locator: "cloud"},
	})
	stop = startScheduler(mounted.Load())
	deadline := time.Now().Add(2 * time.Second)
	for fp.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if fp.count() == 0 {
		t.Fatal("enabled scheduler must have run at least one sweep within 2s")
	}
}
