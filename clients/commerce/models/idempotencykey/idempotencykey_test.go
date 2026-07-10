package idempotencykey

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

func nsDB(parent context.Context, ns string) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(parent, ns))
}

func TestBegin_FirstThenReplay(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	rec, replay, err := Begin(db, "refund:ord_1", "key_abc")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if replay {
		t.Fatal("first Begin reported replay=true; want false")
	}
	if rec.Status != StatusStarted {
		t.Fatalf("status = %q; want started", rec.Status)
	}

	// Complete records the response.
	if err := Complete(rec, `{"ok":true,"refunded":1500}`); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// A replay returns the stored completed record + response.
	rec2, replay2, err := Begin(db, "refund:ord_1", "key_abc")
	if err != nil {
		t.Fatalf("begin replay: %v", err)
	}
	if !replay2 {
		t.Fatal("second Begin reported replay=false; want true")
	}
	if rec2.Status != StatusCompleted {
		t.Fatalf("replay status = %q; want completed", rec2.Status)
	}
	if rec2.Response != `{"ok":true,"refunded":1500}` {
		t.Fatalf("replay response = %q; not persisted", rec2.Response)
	}
}

func TestBegin_DifferentScopeSameKeyNoCollision(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	_, replayA, _ := Begin(db, "refund:ord_1", "same_key")
	_, replayB, _ := Begin(db, "refund:ord_2", "same_key") // different scope
	if replayA || replayB {
		t.Fatalf("distinct scopes collided: A=%v B=%v", replayA, replayB)
	}
}

// TestBegin_ConcurrentSameKey proves the deterministic id collapses concurrent
// first-time Begins to a SINGLE stored record (the ledger never forks), even
// though the read-then-write may let more than one caller see "not started".
func TestBegin_ConcurrentSameKey(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			db := nsDB(c, "acme")
			_, _, _ = Begin(db, "refund:ord_race", "race_key")
		}()
	}
	wg.Wait()

	db := nsDB(c, "acme")
	recs := make([]*IdempotencyKey, 0, n)
	if _, err := Query(db).Filter("IdemKey=", "race_key").GetAll(&recs); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("concurrent Begin created %d records; want 1 (deterministic id must collapse them)", len(recs))
	}
}

func TestBegin_TenantIsolation(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	acme := nsDB(c, "acme")
	_, _, _ = Begin(acme, "refund:ord_1", "shared")
	rec, _ := New(acme), 0
	_ = rec
	// Same scope+key in beta is a DIFFERENT record (namespace-scoped id column).
	beta := nsDB(c, "beta")
	_, replay, err := Begin(beta, "refund:ord_1", "shared")
	if err != nil {
		t.Fatalf("beta begin: %v", err)
	}
	if replay {
		t.Fatal("beta saw acme's idempotency record — tenant isolation broken")
	}
}

// TestBegin_FreshStartedIsInFlight proves a not-yet-completed guard that is
// still fresh reports replay=true (in-flight) so the caller fails closed (409)
// rather than running a second concurrent money move.
func TestBegin_FreshStartedIsInFlight(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	rec, replay, err := Begin(db, "refund:ord_x", "k")
	if err != nil || replay {
		t.Fatalf("first begin: err=%v replay=%v", err, replay)
	}
	if rec.Recoverable() {
		t.Fatal("a just-created started guard must NOT be recoverable")
	}

	// Second Begin while still fresh + not completed → in-flight replay.
	_, replay2, err := Begin(db, "refund:ord_x", "k")
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if !replay2 {
		t.Fatal("fresh started guard: second Begin must report replay=true (in-flight)")
	}
}

// TestBegin_StaleStartedRecovers proves a crashed (stale, never-completed) guard
// is re-claimed so a retry can proceed — the money move's deterministic gateway
// key makes the retry safe. Uses the nowFn clock seam to simulate elapsed time.
func TestBegin_StaleStartedRecovers(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	if _, replay, err := Begin(db, "refund:ord_y", "k"); err != nil || replay {
		t.Fatalf("seed started guard: err=%v replay=%v", err, replay)
	}

	// Jump the clock past StartedTTL — the guard now looks crashed.
	orig := nowFn
	nowFn = func() time.Time { return orig().Add(StartedTTL + time.Minute) }
	defer func() { nowFn = orig }()

	rec, replay, err := Begin(db, "refund:ord_y", "k")
	if err != nil {
		t.Fatalf("recover begin: %v", err)
	}
	if replay {
		t.Fatal("stale started guard must be RE-CLAIMED (replay=false) so the caller can retry, not stuck at 409 forever")
	}
	if rec.Status != StatusStarted {
		t.Fatalf("re-claimed guard status = %q, want started", rec.Status)
	}

	// Completing it now works and future replays return the response.
	if err := Complete(rec, `{"ok":true}`); err != nil {
		t.Fatalf("complete after recovery: %v", err)
	}
	got, replay3, _ := Begin(db, "refund:ord_y", "k")
	if !replay3 || got.Status != StatusCompleted || got.Response != `{"ok":true}` {
		t.Fatalf("post-recovery replay: replay=%v status=%q resp=%q", replay3, got.Status, got.Response)
	}
}
