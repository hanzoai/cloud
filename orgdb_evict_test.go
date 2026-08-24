package cloud

// orgdb_evict_test.go — THE OPEN SET IS BOUNDED, AND AN EVICTION IS A DRAIN.
//
// OrgStore held every entity it was ever asked for, for the life of the process:
// no eviction, no ceiling, and CloseAll — the only thing that closed anything —
// terminal and reached only at shutdown. So a host's resident cost tracked how
// many tenants had EVER touched it rather than how many were active, which is the
// opposite of the property the durable plane exists to buy. An org's state is
// re-derivable from the object store; that is what makes a pod disposable, and
// keeping every org open forever spends it.
//
// Bounding it is the easy half. The half that has to be right is what an eviction
// DOES: it takes the same path a graceful drain takes — the Durable's own Close,
// which ships the final state fenced at the lease round and releases ownership —
// because a handle closed out from under the fence strands the org on a replica
// that is no longer serving it.

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
)

// TestEvictionLosesNoAckedWrite is the property, in the shape rollingupgrade_test
// proves for a rollout: writes are acknowledged continuously while the store is
// forced to evict, and every acknowledged write is still there afterwards.
//
// Eviction is forced by ASKING for more entities than the bound, which is what a
// multi-tenant host does by existing. Each write is counted only after its commit
// returns, so the tally is of ACKNOWLEDGED writes and not of attempts — a write
// that failed is a write nobody was told about, and only the ones a caller was
// told succeeded are owed.
func TestEvictionLosesNoAckedWrite(t *testing.T) {
	const (
		orgs   = 40
		bound  = 8
		writes = 25
		// Long enough that no goroutine here is descheduled past it while holding a
		// handle — which is the invariant the whole design rests on — and short
		// enough that an org which has finished writing ages out during the run.
		idle = 500 * time.Millisecond
	)
	dir := t.TempDir()
	store := NewOrgStore(Base{DataDir: dir}, "widget", openRows)
	store.maxOpen = bound
	store.idleAfter = idle
	t.Cleanup(func() { _ = store.CloseAll() })

	var mu sync.Mutex
	acked := map[string]int{} // org -> writes it was TOLD succeeded

	var wg sync.WaitGroup
	for i := range orgs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("org%02d", i)
			ns := MustOrgNamespace(name, "")
			for w := range writes {
				db, err := store.For(ns)
				if err != nil {
					t.Errorf("For(%s): %v", name, err)
					return
				}
				if _, err := db.Exec(`INSERT INTO rows(v) VALUES(?)`, w); err != nil {
					// A handle closed under a live writer. It must not happen — the
					// store was touched microseconds ago and the deadline is 500ms —
					// so it is an error and not a tolerated race.
					t.Errorf("write %d to %s: %v", w, name, err)
					return
				}
				mu.Lock()
				acked[name]++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// Every writer is done, so every store is now idle. Age past the deadline and
	// drive reclaim, which is what a host does between bursts.
	time.Sleep(idle + 50*time.Millisecond)
	if _, err := store.For(MustOrgNamespace("trigger", "")); err != nil {
		t.Fatal(err)
	}

	if got := store.evictions.Load(); got == 0 {
		t.Fatal("nothing was evicted, so this proves nothing about eviction")
	} else {
		t.Logf("%d evictions across %d orgs at a bound of %d", got, orgs, bound)
	}
	store.mu.Lock()
	open := len(store.byNS)
	store.mu.Unlock()
	if open > bound {
		t.Errorf("%d stores open after reclaim, target is %d", open, bound)
	}

	// AND EVERY ACKNOWLEDGED WRITE SURVIVED. Reading through For re-opens whatever
	// was evicted, which is the other half of the claim: a bound that loses data is
	// not a bound worth having.
	total := 0
	for name, want := range acked {
		db, err := store.For(MustOrgNamespace(name, ""))
		if err != nil {
			t.Fatalf("re-open %s: %v", name, err)
		}
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM rows`).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s holds %d rows, was told %d writes succeeded", name, got, want)
		}
		total += got
	}
	if total != orgs*writes {
		t.Fatalf("%d rows across %d orgs, want %d", total, orgs, orgs*writes)
	}
}

// TestEvictionTakesTheLeastRecentlyUsed: the bound is a cache, so which entity it
// drops decides whether it saves anything. An org asked for on every pass must
// survive a flood of orgs asked for once, or the flood evicts the working set and
// the host thrashes — paying a re-open for the very store it is about to be asked
// for again.
func TestEvictionTakesTheLeastRecentlyUsed(t *testing.T) {
	dir := t.TempDir()
	store := NewOrgStore(Base{DataDir: dir}, "widget", openRows)
	store.maxOpen = 4
	store.idleAfter = 20 * time.Millisecond
	t.Cleanup(func() { _ = store.CloseAll() })

	hot := MustOrgNamespace("hot", "")
	if _, err := store.For(hot); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		// Each cold org is asked for once and then left alone, so it ages past the
		// deadline while the hot one is re-touched on every pass.
		if _, err := store.For(MustOrgNamespace(fmt.Sprintf("cold%02d", i), "")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := store.For(hot); err != nil { // keep it hot
			t.Fatal(err)
		}
	}

	store.mu.Lock()
	_, stillOpen := store.byNS[hot]
	store.mu.Unlock()
	if !stillOpen {
		t.Fatal("the hot store was evicted while 20 one-shot orgs went past — reclaim is taking its own working set")
	}
	if store.evictions.Load() == 0 {
		t.Fatal("nothing was evicted, so the survival of the hot store proves nothing")
	}
}

// TestReclaimHoldsALiveStoreOverTheBound is the direction the first draft got
// wrong, kept as a test so it cannot be got wrong again. Every store is in use,
// so none is past the deadline, so the open set legitimately exceeds maxOpen —
// and reclaim leaves them alone rather than closing a database out from under a
// caller. Memory over the target is recoverable; a lost write is not.
func TestReclaimHoldsALiveStoreOverTheBound(t *testing.T) {
	store := NewOrgStore(Base{DataDir: t.TempDir()}, "widget", openRows)
	store.maxOpen = 2
	store.idleAfter = time.Hour // nothing can age out during this test
	t.Cleanup(func() { _ = store.CloseAll() })

	var handles []*sql.DB
	for i := range 10 {
		db, err := store.For(MustOrgNamespace(fmt.Sprintf("live%02d", i), ""))
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, db)
	}
	// Every handle still works: none was closed to satisfy the bound.
	for i, db := range handles {
		if _, err := db.Exec(`INSERT INTO rows(v) VALUES(?)`, i); err != nil {
			t.Fatalf("handle %d was closed to honour the bound: %v", i, err)
		}
	}
	if store.evictions.Load() != 0 {
		t.Fatalf("%d evictions with nothing idle — reclaim took a live store", store.evictions.Load())
	}
}

// TestEvictionShipsAndReleasesRatherThanClosing is the safety property, and it is
// the reason eviction is not one line.
//
// On the durable plane an open store OWNS the org: it holds a fence lease and its
// local file is the working copy of an object in the store. Dropping the handle
// alone would leave that object as it was at the last ship and the lease held by a
// replica that is no longer serving the org. So an eviction runs the same Close
// the drain runs — a final fenced ship, then release — and this asserts the
// OBSERVABLE consequence of that: the object store received a write for the
// evicted org, and re-opening it hydrates that state back.
func TestEvictionShipsAndReleasesRatherThanClosing(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{}}
	members := org.NewMembership("pod-0", org.StaticSource(org.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	// WithCheckpoint is what buildDurability passes in production, and without it
	// the ship reads a main database file whose rows are still in the WAL — the
	// snapshot read simply fails. It is part of the mechanism, not a tuning option.
	dur := org.NewDurability(cond, members, nil, org.WithSeal(sqlitedrv.Checkpoint), org.WithReader(orgReader))

	store := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget", openRows)
	store.maxOpen = 1
	store.idleAfter = 0 // single-threaded here: nothing else holds a handle
	t.Cleanup(func() { _ = store.CloseAll() })

	victim := MustOrgNamespace("evicted", "")
	db, err := store.For(victim)
	if err != nil {
		t.Fatalf("For(evicted): %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rows(v) VALUES(1)`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.Sync(victim); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	before := cond.writes()

	// One more entity at a bound of one forces the eviction.
	time.Sleep(time.Millisecond)
	if _, err := store.For(MustOrgNamespace("successor", "")); err != nil {
		t.Fatalf("For(successor): %v", err)
	}
	store.mu.Lock()
	_, stillOpen := store.byNS[victim]
	store.mu.Unlock()
	if stillOpen {
		t.Fatal("the victim is still open — nothing was evicted, so this test proves nothing about eviction")
	}
	if got := cond.writes(); got <= before {
		t.Fatalf("the object store took %d writes before the eviction and %d after: the handle was closed without shipping, which strands the org's last state and its lease", before, got)
	}

	// And the state comes back: a re-open hydrates what the eviction shipped.
	db2, err := store.For(victim)
	if err != nil {
		t.Fatalf("re-open evicted: %v", err)
	}
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM rows`).Scan(&n); err != nil {
		t.Fatalf("count after re-open: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-opened store holds %d rows, want the 1 that was written before eviction", n)
	}
}

// TestReclaimIsOffWithoutABound keeps the escape hatch honest: maxOpen 0 is the
// prior unbounded behaviour, so a caller that genuinely wants every entity
// resident says so rather than discovering the bound.
func TestReclaimIsOffWithoutABound(t *testing.T) {
	store := NewOrgStore(Base{DataDir: t.TempDir()}, "widget", openRows)
	store.maxOpen = 0
	t.Cleanup(func() { _ = store.CloseAll() })
	for i := range 12 {
		if _, err := store.For(MustOrgNamespace(fmt.Sprintf("org%02d", i), "")); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	open := len(store.byNS)
	store.mu.Unlock()
	if open != 12 {
		t.Fatalf("%d open with no bound, want all 12", open)
	}
}

// openRows is the subsystem's own open func: one table, so a write is a real
// SQLite commit through the real cek path rather than a stand-in.
func openRows(db *sql.DB) (*sql.DB, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS rows(v INTEGER)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// recordingStore is a ConditionalStore in memory that COUNTS writes, which is how
// the eviction's ship is observed. It implements the CAS contract faithfully —
// version mismatch is a conflict — because the fence is what makes a racing
// re-open safe and a store that accepted every put would hide a broken one.
type recordingStore struct {
	mu      sync.Mutex
	objects map[string]versioned
	puts    int
	seq     int
}

type versioned struct {
	data    []byte
	version string
}

func (s *recordingStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, "", fmt.Errorf("recordingStore get %q: %w", key, replica.ErrNotFound)
	}
	return o.data, o.version, nil
}

func (s *recordingStore) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.objects[key]
	switch {
	case expect == "" && exists:
		return "", fmt.Errorf("recordingStore put %q: %w", key, replica.ErrConflict)
	case expect != "" && (!exists || cur.version != expect):
		return "", fmt.Errorf("recordingStore put %q: %w", key, replica.ErrConflict)
	}
	s.seq++
	s.puts++
	v := fmt.Sprintf("v%d", s.seq)
	s.objects[key] = versioned{data: append([]byte(nil), data...), version: v}
	return v, nil
}

func (s *recordingStore) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}
