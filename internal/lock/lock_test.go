package lock

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// One key admits one holder at a time, other keys do not wait on it, and a key
// nobody holds keeps nothing.
func TestKeyed(t *testing.T) {
	var k Keyed
	var in, most int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := k.Hold("job")
			mu.Lock()
			in++
			most = max(most, in)
			mu.Unlock()
			mu.Lock()
			in--
			mu.Unlock()
			release()
		}()
	}
	release := k.Hold("other")
	other := k.Hold("third") // a different key does not wait on "other"
	other()
	release()
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d held one key at once", most)
	}
	if len(k.m) != 0 {
		t.Fatalf("keys nobody holds are kept: %v", k.m)
	}
}

// A caller that gives up while it waits holds nothing, leaves nothing behind for the
// key, and does not take the key from its holder.
func TestTakeGivesUpWithItsContext(t *testing.T) {
	var k Keyed
	release := k.Hold("job")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if again, err := k.Take(ctx, "job"); err == nil {
		again()
		t.Fatal("a second caller took a key that is held")
	}
	if n := k.Refs("job"); n != 1 {
		t.Fatalf("%d callers hold or wait for the key, want its holder alone", n)
	}
	release()
	release() // again, as a deferred release and an explicit one both do
	if len(k.m) != 0 {
		t.Fatalf("keys nobody holds are kept: %v", k.m)
	}
	free, err := k.Take(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	free()
}

// parked waits until a caller is waiting for a lock on db's database: on PostgreSQL a
// backend asking for an advisory lock it has not been granted.
func parked(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing ever asked for the lock")
}

// A key is held by one replica at a time, another key is nobody's, and a key is free
// again the moment its holder lets it go. A second replica is a second pool on the
// same store, which is what another pod is; a file belongs to one process, where the
// holder's Keyed is the lock.
func TestAcrossHoldsAKeyAgainstAnotherReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns := namespace.System()
		first, second := open(ns, "locks"), open(ns, "locks")
		var one, other Keyed
		ctx := context.Background()

		release, err := one.Across(ctx, first, "job")
		if err != nil {
			t.Fatal(err)
		}
		if !dialect.Of(first).Postgres() {
			release()
			return
		}
		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		if again, err := other.Across(short, second, "job"); err == nil {
			again()
			t.Fatal("a second replica took a key the first holds")
		}
		third, err := other.Across(ctx, second, "other")
		if err != nil {
			t.Fatalf("another key waited: %v", err)
		}
		third()

		got := make(chan error, 1)
		go func() {
			rel, err := other.Across(ctx, second, "job")
			if err == nil {
				rel()
			}
			got <- err
		}()
		parked(t, first)
		select {
		case err := <-got:
			t.Fatalf("a replica went through a key another holds: %v", err)
		default:
		}
		release()
		if err := <-got; err != nil {
			t.Fatalf("the key was not freed when its holder let it go: %v", err)
		}
	})
}

// Many callers on several replicas take a key one at a time: of the work each does
// under it, none overlaps another's.
func TestAcrossTakesAKeyInTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns := namespace.System()
		dbs := []*sql.DB{open(ns, "locks")}
		if dialect.Of(dbs[0]).Postgres() {
			dbs = append(dbs, open(ns, "locks"), open(ns, "locks"))
		}
		var in, worst int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, db := range dbs {
			var replica Keyed
			for range 3 {
				wg.Go(func() {
					release, err := replica.Across(context.Background(), db, "lead")
					if err != nil {
						t.Errorf("lock: %v", err)
						return
					}
					mu.Lock()
					in++
					worst = max(worst, in)
					mu.Unlock()
					time.Sleep(5 * time.Millisecond)
					mu.Lock()
					in--
					mu.Unlock()
					release()
				})
			}
		}
		wg.Wait()
		if worst != 1 {
			t.Fatalf("%d holders at once under one key", worst)
		}
	})
}

// A Keyed holds two keys against a database at once and no more: the third waits
// without a connection, so the holders keep the pool they need to finish, and goes
// through when one lets go.
func TestAcrossHoldsAtMostTwoKeysAtOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := open(namespace.System(), "locks")
		if !dialect.Of(db).Postgres() {
			return // a file holds nothing but its mutex
		}
		var k Keyed
		ctx := context.Background()
		a, err := k.Across(ctx, db, "a")
		if err != nil {
			t.Fatal(err)
		}
		b, err := k.Across(ctx, db, "b")
		if err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if c, err := k.Across(short, db, "c"); err == nil {
			c()
			t.Fatal("a third key was held beside two")
		}
		if n := db.Stats().InUse; n > 2 {
			t.Fatalf("%d connections in use for two keys", n)
		}
		a()
		c, err := k.Across(ctx, db, "c")
		if err != nil {
			t.Fatalf("a key waited after a holder let go: %v", err)
		}
		b()
		c()
	})
}

// A caller with no store holds its key on the process's lock pool: another replica, which
// is another Keyed beside the same database, is kept out of it, other keys are nobody's,
// and the pool is one for the process and not one per key. A file has one process, where
// the Keyed is the whole lock.
func TestAcrossHoldsAKeyOnTheLockPool(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pool, err := locus.Locks()
		if err != nil {
			t.Fatal(err)
		}
		var one, other Keyed
		ctx := context.Background()
		release, err := one.Across(ctx, nil, "amend:acme:ref")
		if err != nil {
			t.Fatal(err)
		}
		if pool == nil {
			release()
			return // a file: no database, no second process
		}
		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		if again, err := other.Across(short, nil, "amend:acme:ref"); err == nil {
			again()
			t.Fatal("a second replica took a key the first holds")
		}
		third, err := other.Across(ctx, nil, "amend:acme:other")
		if err != nil {
			t.Fatalf("another key waited: %v", err)
		}
		third()
		got := make(chan error, 1)
		go func() {
			rel, err := other.Across(ctx, nil, "amend:acme:ref")
			if err == nil {
				rel()
			}
			got <- err
		}()
		parked(t, pool)
		select {
		case err := <-got:
			t.Fatalf("a replica went through a key another holds: %v", err)
		default:
		}
		release()
		if err := <-got; err != nil {
			t.Fatalf("the key was not freed when its holder let it go: %v", err)
		}
		if again, _ := locus.Locks(); again != pool {
			t.Fatal("a second call made a second lock pool")
		}
	})
}
