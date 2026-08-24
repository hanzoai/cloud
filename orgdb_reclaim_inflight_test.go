package cloud

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/org"
)

// Reclaim bounds byNS. Nothing bounded inflight, and the two are not the same map.
//
// An open publishes into byNS and is then a reclaim candidate like any other; an
// open IN PROGRESS lives in inflight, which reclaim never reads and never prunes.
// So inflight is bounded only by the promise that every open retires its own
// record on the way out — which is a property of forNS, not of the eviction
// policy, and until forNS deferred that step it was a promise the panic path did
// not keep. A leaked record is worse than a leaked handle: it is the thing every
// later caller for that namespace WAITS on.
//
// This asserts the two together, because they are only correct together: the open
// set stays inside the bound, evictions actually happened (a bound that held
// because nothing needed evicting proves nothing), and inflight is EMPTY at rest.
func TestReclaimBoundsTheOpenSetAndLeavesNothingInFlight(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{}}
	members := org.NewMembership("pod-0", org.StaticSource(org.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	dur := org.NewDurability(cond, members, nil, org.WithCheckpoint(durableCheckpoint))

	store := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget", openRows)
	store.maxOpen = 2
	store.idleAfter = 0 // single-threaded: nothing else is holding a handle
	t.Cleanup(func() { _ = store.CloseAll() })

	for i := range 8 {
		ns := MustOrgNamespace(string(rune('a'+i))+"corp", "")
		if _, err := store.For(ns); err != nil {
			t.Fatalf("For %s: %v", ns, err)
		}
	}

	store.mu.Lock()
	open, held, pending := len(store.byNS), len(store.durables), len(store.inflight)
	store.mu.Unlock()

	if open > store.maxOpen {
		t.Errorf("open set is %d, over the bound of %d", open, store.maxOpen)
	}
	if held != open {
		t.Errorf("durables holds %d for %d open stores — the two maps are parallel", held, open)
	}
	// A bound that held because nothing needed evicting is a different green run.
	if n := store.evictions.Load(); n == 0 {
		t.Error("nothing was evicted, so this proves the bound and not the eviction")
	}
	if pending != 0 {
		t.Errorf("inflight holds %d records at rest — every one is a channel a later caller for that namespace waits on forever", pending)
	}
}

// The same invariant on the path that used to break it. A failed open must retire
// its record too, or the namespace it failed on is wedged for the process lifetime
// — and reclaim will not clean it up, because reclaim reads byNS and this record
// is not there.
func TestAFailedOpenLeavesNothingInFlight(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{}}
	members := org.NewMembership("pod-0", org.StaticSource(org.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	dur := org.NewDurability(cond, members, nil, org.WithCheckpoint(durableCheckpoint))

	explode := true
	store := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget", func(db *sql.DB) (*sql.DB, error) {
		if explode {
			explode = false
			panic("open exploded")
		}
		return db, nil
	})
	t.Cleanup(func() { _ = store.CloseAll() })

	ns := MustOrgNamespace("wedged", "")
	func() {
		defer func() { _ = recover() }()
		_, _ = store.For(ns)
	}()

	store.mu.Lock()
	pending := len(store.inflight)
	store.mu.Unlock()
	if pending != 0 {
		t.Fatalf("inflight holds %d records after a failed open — that namespace is wedged", pending)
	}
}
