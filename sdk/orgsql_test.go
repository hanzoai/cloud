package cloud

// orgsql_test.go — THE SEAM: one OrgStore, two engines.
//
// OrgDB and OrgStore choose their engine from CLOUD_SQL_DSN, and everything above
// them (a store's SQL, its migration, its tests) is written once. These tests hold
// the seam itself to the same behaviour on both: a store per entity, isolated, found
// by Has and Each, evicted and reopened without losing a row. And hold the PostgreSQL
// side to the claim that makes it a different thing from a file: it leaves nothing on
// the disk and ships nothing.

import (
	"database/sql"
	"fmt"
	"github.com/hanzoai/cloud/manifest"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
)

// openCounted is a subsystem's open func: one table, a migration that counts how
// many times it ran.
func openCounted(runs *atomic.Int32) func(*sql.DB) (*sql.DB, error) {
	return func(db *sql.DB) (*sql.DB, error) {
		runs.Add(1)
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS rows (v BIGINT NOT NULL)`); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
}

func TestOrgStoreOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		onSQL := locus.DSN() != ""
		dir := t.TempDir()
		var runs atomic.Int32
		stores := NewOrgStore(Base{DataDir: dir, Peers: true}, "flags", openCounted(&runs))
		t.Cleanup(func() { _ = stores.CloseAll() })

		acme, globex := principal.MustOrgNamespace("acme", ""), principal.MustOrgNamespace("globex", "")
		if stores.Has(acme) {
			t.Fatal("Has found a store nobody opened")
		}
		if stores.Stored() {
			t.Fatal("Stored is true before anything opened")
		}
		a, err := stores.For(acme)
		if err != nil {
			t.Fatal(err)
		}
		g, err := stores.For(globex)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Exec(`INSERT INTO rows (v) VALUES ($1)`, 7); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := g.QueryRow(`SELECT COUNT(*) FROM rows`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("CROSS-TENANT: globex sees %d of acme's rows, %v", n, err)
		}
		if again, _ := stores.For(acme); again != a || runs.Load() != 2 {
			t.Fatalf("a second For reopened the store (%d migrations)", runs.Load())
		}
		if !stores.Has(acme) || stores.Has(principal.MustOrgNamespace("initech", "")) || !stores.Stored() {
			t.Fatal("Has / Stored disagree with what was opened")
		}

		// A sweep visits what was asked for.
		var seen []string
		if err := stores.Each(func(ns namespace.Namespace, _ *sql.DB, err error) {
			if err != nil {
				t.Errorf("Each(%s): %v", ns, err)
			}
			seen = append(seen, ns.ID())
		}); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen[0] != "acme" || seen[1] != "globex" {
			t.Fatalf("Each visited %v", seen)
		}

		// Sync is the commit's own acknowledgement on hanzo-sql, even on a deployment
		// that declares several writers and has no object store to fence them.
		acked, err := stores.Sync(acme)
		if onSQL && (err != nil || !acked) {
			t.Fatalf("Sync on hanzo-sql = %v, %v; a commit is the durable write", acked, err)
		}
		if !onSQL && err == nil {
			t.Fatal("Sync on a multi-writer deployment with no fence acknowledged a write")
		}

		if onSQL {
			// No file, no marker, no sidecar: the database is the only place anything is.
			if ents, _ := os.ReadDir(dir); len(ents) != 0 {
				t.Fatalf("hanzo-sql left %d entries under the data dir", len(ents))
			}
		}
	})
}

// A restarted process knows its stores from the registry alone, with an empty disk.
func TestOrgStoreSweepsWhatARestartLost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("a file's pending set is its markers; the registry is hanzo-sql's")
		}
		var runs atomic.Int32
		first := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		for _, org := range []string{"acme", "globex"} {
			st, err := first.For(principal.MustOrgNamespace(org, ""))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Exec(`INSERT INTO rows (v) VALUES (1)`); err != nil {
				t.Fatal(err)
			}
		}
		_ = first.CloseAll()

		second := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		t.Cleanup(func() { _ = second.CloseAll() })
		if !second.Stored() || !second.Has(principal.MustOrgNamespace("acme", "")) {
			t.Fatal("a restarted process did not find the stores in the registry")
		}
		rows := 0
		if err := second.Each(func(ns namespace.Namespace, st *sql.DB, err error) {
			if err != nil {
				t.Errorf("Each(%s): %v", ns, err)
				return
			}
			var n int
			if err := st.QueryRow(`SELECT COUNT(*) FROM rows`).Scan(&n); err != nil {
				t.Errorf("%s: %v", ns, err)
			}
			rows += n
		}); err != nil {
			t.Fatal(err)
		}
		if rows != 2 {
			t.Fatalf("the sweep after a restart read %d rows, want 2", rows)
		}
		if n := len(second.byNS); n != 0 {
			t.Fatalf("the sweep kept %d stores open; it must open, use and release each", n)
		}
	})
}

// Concurrent first requests for one org open it once.
func TestOrgStoreOpensOnceUnderLoad(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var runs atomic.Int32
		stores := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		t.Cleanup(func() { _ = stores.CloseAll() })
		var wg sync.WaitGroup
		handles := make(chan *sql.DB, 32)
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, err := stores.For(principal.MustOrgNamespace("acme", ""))
				if err != nil {
					t.Error(err)
					return
				}
				handles <- st
			}()
		}
		wg.Wait()
		close(handles)
		var first *sql.DB
		for h := range handles {
			if first == nil {
				first = h
			}
			if h != first {
				t.Fatal("two handles for one org")
			}
		}
		if runs.Load() != 1 {
			t.Fatalf("the migration ran %d times for one org", runs.Load())
		}
	})
}

// Eviction closes a store's pool and loses nothing: the rows are the database's, and
// the next For opens the store again.
func TestOrgStoreEvictionKeepsTheRows(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var runs atomic.Int32
		stores := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		stores.maxOpen = 2
		stores.idleAfter = time.Nanosecond
		t.Cleanup(func() { _ = stores.CloseAll() })

		for i := range 6 {
			ns := principal.MustOrgNamespace(fmt.Sprintf("org%d", i), "")
			st, err := stores.For(ns)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Exec(`INSERT INTO rows (v) VALUES ($1)`, i); err != nil {
				t.Fatal(err)
			}
		}
		if stores.evictions.Load() == 0 {
			t.Fatal("nothing was evicted, so the bound was not exercised")
		}
		for i := range 6 {
			st, err := stores.For(principal.MustOrgNamespace(fmt.Sprintf("org%d", i), ""))
			if err != nil {
				t.Fatal(err)
			}
			var v int
			if err := st.QueryRow(`SELECT v FROM rows`).Scan(&v); err != nil || v != i {
				t.Fatalf("org%d after eviction: %d, %v", i, v, err)
			}
		}
	})
}

// A store that cannot be named (a user is not an org) is refused before any
// connection is made, on either engine.
func TestOrgStoreRefusesWhatHasNoStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		stores := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = stores.CloseAll() })
		if _, err := stores.For(namespace.MustUser("u1")); err == nil {
			t.Fatal("a user namespace opened a store")
		}
	})
}

// The registry holds a namespace as text, and OrgOnServer returns exactly the
// namespace that was rendered, for every kind a store can be named after.
func TestOrgOnServerReadsBackWhatTheRegistryHolds(t *testing.T) {
	for _, ns := range []namespace.Namespace{
		principal.MustOrgNamespace("acme", ""),
		principal.MustOrgNamespace("acme", "web"),
		namespace.System(),
	} {
		got, err := principal.OrgOnServer(ns.String())
		if err != nil || got != ns {
			t.Errorf("OrgOnServer(%q) = %v, %v", ns.String(), got, err)
		}
	}
	if _, err := principal.OrgOnServer("not a namespace"); err == nil {
		t.Error("garbage was read as a namespace")
	}
}

// With the DSN set, a subsystem that has not been ported stays its file: its SQL is
// SQLite's and is not run on PostgreSQL, and nothing about it needs the server. The one
// thing refused is a file for a store the registry has on the server, which would split it.
func TestAnUnportedSubsystemStaysItsFileWithTheDSNSet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		dir := t.TempDir()
		acme := principal.MustOrgNamespace("acme", "")
		stores := NewOrgStore(Base{DataDir: dir}, "todo", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = stores.CloseAll() })
		st, err := stores.For(acme)
		if err != nil {
			t.Fatalf("an unported subsystem was refused: %v", err)
		}
		if _, err := st.Exec(`INSERT INTO rows (v) VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		// Closed, because a file under the pure-Go codec is written back when its handle is.
		if err := stores.CloseAll(); err != nil {
			t.Fatal(err)
		}
		path, _ := namespace.Path(dir, acme, "todo")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("an unported subsystem's store has no file: %v", err)
		}
		if ok, err := locus.Exists(acme, "todo"); err != nil || ok {
			t.Fatalf("an unported subsystem's store is on the server (%v, %v)", ok, err)
		}

		// A store of it that the registry has on the server is not opened as a file.
		elsewhere, err := locus.Open(acme, "todo", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_ = elsewhere.Close()
		other := NewOrgStore(Base{DataDir: dir}, "todo", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = other.CloseAll() })
		if _, err := other.For(principal.MustOrgNamespace("acme", "web")); err != nil {
			t.Fatalf("another store of the unported subsystem was refused: %v", err)
		}
		again := NewOrgStore(Base{DataDir: dir}, "todo", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = again.CloseAll() })
		if _, err := again.For(acme); err == nil || !strings.Contains(err.Error(), "split") {
			t.Fatalf("a file was opened for a store the registry has on the server: %v", err)
		}
		for name := range manifest.SQLStores {
			if _, err := OrgDB(t.TempDir(), principal.MustOrgNamespace("acme", ""), name); err != nil {
				t.Errorf("the ported subsystem %q was refused: %v", name, err)
			}
		}
	})
}

// 400 stores to sweep, and the sweep opens one at a time: the server never sees more
// connections than the process's budget, and nothing is left open when it is done.
func TestASweepOfFourHundredStoresOpensUsesAndReleases(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		var runs atomic.Int32
		maker := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		for i := range 400 {
			st, err := maker.For(principal.MustOrgNamespace(fmt.Sprintf("org%03d", i), ""))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Exec(`INSERT INTO rows (v) VALUES ($1)`, i); err != nil {
				t.Fatal(err)
			}
		}
		_ = maker.CloseAll()

		sweeper := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(&runs))
		t.Cleanup(func() { _ = sweeper.CloseAll() })
		stop := sqltest.Peak(t, locus.DSN())
		sum, visited := 0, 0
		if err := sweeper.Each(func(ns namespace.Namespace, st *sql.DB, err error) {
			if err != nil {
				t.Errorf("Each(%s): %v", ns, err)
				return
			}
			var v int
			if err := st.QueryRow(`SELECT v FROM rows`).Scan(&v); err != nil {
				t.Errorf("%s: %v", ns, err)
			}
			sum += v
			visited++
		}); err != nil {
			t.Fatal(err)
		}
		high := stop()
		if visited != 400 || sum != 400*399/2 {
			t.Fatalf("the sweep visited %d stores and summed %d", visited, sum)
		}
		if high > postgres.Budget() {
			t.Fatalf("the server saw %d connections from one process with a budget of %d", high, postgres.Budget())
		}
		if n := len(sweeper.byNS); n != 0 {
			t.Fatalf("%d stores are still open after the sweep", n)
		}
		t.Logf("400 stores swept, budget %d, server peak %d", postgres.Budget(), high)
	})
}

// Two replicas sweeping one store: one of them owns it. The other takes over when the
// first lets go, and when the first's term lapses without a word.
func TestTwoReplicasDoNotBothOwnAStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ns := principal.MustOrgNamespace("acme", "")
		replica := func() *OrgStore[*sql.DB] {
			r := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
			t.Cleanup(func() { _ = r.CloseAll() })
			if _, err := r.For(ns); err != nil {
				t.Fatal(err)
			}
			return r
		}
		a, b := replica(), replica()

		if !a.Owned(ns) {
			t.Fatal("the first replica to ask does not own the store")
		}
		if b.Owned(ns) {
			t.Fatal("two replicas own one store")
		}
		if !a.Owned(ns) || b.Owned(ns) {
			t.Fatal("ownership changed hands without a reason")
		}

		// The owner shuts down. The refusal b was given is remembered for a few seconds
		// (denialMax), so the test lets it lapse as the seconds would.
		_ = a.CloseAll()
		b.leaseMu.Lock()
		delete(b.denied, ns)
		b.leaseMu.Unlock()
		if !b.Owned(ns) {
			t.Fatal("a released store was not taken over")
		}
		// And the replica that let go does not go on saying yes: its grant is forgotten
		// with the lease, not left to run out.
		if a.Owned(ns) {
			t.Fatal("a replica that was closed still owns a store the other has taken")
		}

		// The new owner goes quiet: its row lapses in the database and its own grant has
		// run out by its own clock. A third replica takes the store; the lapsed owner,
		// asked again, finds it is not its to act on.
		admin, err := sql.Open("pgx", locus.DSN())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close() }()
		if _, err := admin.Exec(`UPDATE public.cloud_leases SET expires = 0`); err != nil {
			t.Fatal(err)
		}
		b.leaseMu.Lock()
		b.leases[ns] = time.Now().Add(-time.Second)
		b.leaseMu.Unlock()
		c := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = c.CloseAll() })
		if !c.Owned(ns) {
			t.Fatal("a lapsed lease was not taken over")
		}
		if b.Owned(ns) {
			t.Fatal("a replica whose lease lapsed went on owning the store")
		}
	})
}

// A replica that cannot renew says no. It only tries in the second half of its term,
// so what it has left is less than the margin every yes carries, and a yes it cannot
// renew is one it would delete a pod on.
func TestAReplicaThatCannotRenewStopsSayingYes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ns := principal.MustOrgNamespace("acme", "")
		a := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = a.CloseAll() })
		if _, err := a.For(ns); err != nil {
			t.Fatal(err)
		}
		if !a.Owned(ns) {
			t.Fatal("the first replica does not own the store")
		}
		// Its grant is down to a few seconds, and the database cannot be reached.
		a.leaseMu.Lock()
		a.leases[ns] = time.Now().Add(5 * time.Second)
		a.leaseMu.Unlock()
		t.Setenv(locus.Env, "postgres://cloud@127.0.0.1:1/x?sslmode=disable")
		t.Setenv("CLOUD_ENV", "test")
		if a.Owned(ns) {
			t.Fatal("a replica with five seconds left and no way to renew went on saying yes")
		}
		// And it does not ask again for every row: the failure is remembered, as a refusal is.
		a.leaseMu.Lock()
		remembered := a.denied[ns]
		a.leaseMu.Unlock()
		if !remembered.After(time.Now()) {
			t.Fatal("a lease that could not be asked for was asked for again on the next row")
		}
	})
}

// A refusal is remembered, so a replica that does not own a store and is asked about
// it for every row of a sweep asks the database once, not once per row; and what it
// asks is a read.
func TestARefusedReplicaDoesNotAskAgainForEveryRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ns := principal.MustOrgNamespace("acme", "")
		owner := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		waiting := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = owner.CloseAll(); _ = waiting.CloseAll() })
		for _, r := range []*OrgStore[*sql.DB]{owner, waiting} {
			if _, err := r.For(ns); err != nil {
				t.Fatal(err)
			}
		}
		if !owner.Owned(ns) {
			t.Fatal("the owner does not own it")
		}
		probe, err := sql.Open("pgx", locus.DSN())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = probe.Close() }()
		asks := func() (n int64) {
			if err := probe.QueryRow(`SELECT COALESCE(SUM(idx_scan), 0)::bigint FROM pg_stat_user_tables WHERE relname = 'cloud_leases'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if waiting.Owned(ns) {
			t.Fatal("the second replica owns it")
		}
		time.Sleep(1500 * time.Millisecond) // a backend reports its statistics about once a second
		before := asks()
		for range 500 {
			if waiting.Owned(ns) {
				t.Fatal("the second replica owns it")
			}
		}
		time.Sleep(1500 * time.Millisecond)
		if used := asks() - before; used > 5 {
			t.Fatalf("500 refused rows asked the database %d times", used)
		}
	})
}

// A store another replica made after this one's first sweep is swept here too, and a
// namespace this one settled is not put back by the registry that still lists it.
func TestTheRegistryIsReadAgainForStoresMadeElsewhere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		mine := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		other := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = mine.CloseAll(); _ = other.CloseAll() })
		acme, globex := principal.MustOrgNamespace("acme", ""), principal.MustOrgNamespace("globex", "")
		if _, err := other.For(acme); err != nil {
			t.Fatal(err)
		}
		sweep := func() []string {
			var seen []string
			_ = mine.Each(func(ns namespace.Namespace, _ *sql.DB, _ error) { seen = append(seen, ns.ID()) })
			return seen
		}
		if got := sweep(); len(got) != 1 || got[0] != "acme" {
			t.Fatalf("first sweep = %v", got)
		}
		if _, err := other.For(globex); err != nil {
			t.Fatal(err)
		}
		if got := sweep(); len(got) != 1 {
			t.Fatalf("the registry was read again inside registryEvery: %v", got)
		}
		mine.mu.Lock()
		mine.sqlLoadedAt = time.Time{} // registryEvery has passed
		mine.mu.Unlock()
		if got := sweep(); len(got) != 2 {
			t.Fatalf("a store made on another replica was not swept: %v", got)
		}
		mine.Settle(acme, time.Now().Add(time.Hour))
		mine.mu.Lock()
		mine.sqlLoadedAt = time.Time{}
		mine.mu.Unlock()
		if got := sweep(); len(got) != 1 || got[0] != "globex" {
			t.Fatalf("a settled namespace came back with the registry: %v", got)
		}
	})
}

// A store nobody has made has nothing to own: asking about it takes no lease, so
// public.cloud_leases does not grow a row for every name a sweep or a stranger can say.
func TestNoStoreNoLease(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		a := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
		t.Cleanup(func() { _ = a.CloseAll() })
		// Prove the server, so the table is there to look in.
		if _, err := a.For(principal.MustOrgNamespace("real", "")); err != nil {
			t.Fatal(err)
		}
		if a.Owned(principal.MustOrgNamespace("nobody", "")) {
			t.Fatal("a store that does not exist is owned")
		}
		admin, err := sql.Open("pgx", locus.DSN())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close() }()
		var n int
		if err := admin.QueryRow(`SELECT COUNT(*) FROM public.cloud_leases`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("asking about a store that does not exist left %d lease rows (%v)", n, err)
		}
		if !a.Owned(principal.MustOrgNamespace("real", "")) {
			t.Fatal("a store that exists is not owned")
		}
	})
}

// An OrgStore that never asked for a lease has nothing to give back, so closing it
// does not reach for the database. A server that accepts and never answers is where
// that shows: the reach would take its whole bound.
func TestClosingAnOrgStoreThatNeverLeasedDialsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }() // held, never answered
		}
	}()
	t.Setenv(locus.Env, fmt.Sprintf("postgres://u@%s/x?sslmode=disable", ln.Addr()))
	t.Setenv("CLOUD_ENV", "test")
	stores := NewOrgStore(Base{DataDir: t.TempDir()}, "flags", openCounted(new(atomic.Int32)))
	began := time.Now()
	if err := stores.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 500*time.Millisecond {
		t.Fatalf("closing an OrgStore that never leased took %v: it reached for the database", took)
	}
}
