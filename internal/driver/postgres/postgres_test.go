package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// A store's schema is a pure function of the store, names one store and fits an
// identifier. The tests below hold the seam to it; this one holds the name.
func TestSchemaNamesOneStore(t *testing.T) {
	acme := namespace.MustOrg("acme")
	a, err := postgres.Schema(acme, "flags")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^s_[a-z2-7]{52}$`).MatchString(a) || len(a) > 63 {
		t.Fatalf("schema %q is not s_ and 52 base32 characters inside the 63-byte limit", a)
	}
	again, _ := postgres.Schema(acme, "flags")
	if a != again {
		t.Fatalf("schema is not stable: %q then %q", a, again)
	}
	seen := map[string]bool{a: true}
	for _, c := range []struct {
		ns  namespace.Namespace
		sub string
	}{
		{namespace.MustOrg("globex"), "flags"},
		{namespace.System(), "flags"},
		{acme.WithGroup(namespace.MustGroup("web")), "flags"},
		{acme.WithGroup(namespace.MustGroup("web-other")), "flags"},
		{acme, "flag"},
		{acme, "experiments"},
	} {
		s, err := postgres.Schema(c.ns, c.sub)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.ns, c.sub, err)
		}
		if seen[s] {
			t.Fatalf("%s/%s shares its schema %s with another store", c.ns, c.sub, s)
		}
		seen[s] = true
	}
}

func TestSchemaRefusesWhatHasNoFile(t *testing.T) {
	if _, err := postgres.Schema(namespace.MustUser("u1"), "flags"); err == nil {
		t.Fatal("a user namespace has no store, so it has no schema")
	}
	if _, err := postgres.Schema(namespace.MustOrg("acme"), ""); err == nil {
		t.Fatal("a store with no subsystem has no schema")
	}
}

// Two namespaces' stores hold their own tables of the same name, on either engine,
// and the same pair opened again reaches the same rows.
func TestOrgIsolatesStores(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		acme, globex := namespace.MustOrg("acme"), namespace.MustOrg("globex")
		a, g := open(acme, "widget"), open(globex, "widget")
		for _, db := range []*sql.DB{a, g} {
			if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS thing (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.Exec(`INSERT INTO thing (id, name) VALUES ($1, $2)`, 1, "acme's"); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := g.QueryRow(`SELECT COUNT(*) FROM thing`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("CROSS-TENANT: globex sees %d of acme's rows (err %v)", n, err)
		}
		// Reopening is closing and opening: two live handles on one SQLite file are two
		// writers, which a store never has.
		_ = a.Close()
		var name string
		if err := open(acme, "widget").QueryRow(`SELECT name FROM thing WHERE id = $1`, 1).Scan(&name); err != nil || name != "acme's" {
			t.Fatalf("the same store opened again lost its row: %q, %v", name, err)
		}
		other := open(acme, "gadget")
		if _, err := other.Exec(`SELECT name FROM thing`); err == nil {
			t.Fatal("a second subsystem of the same org reached the first one's table")
		}
	})
}

// The pool a store gets is small and keeps at most one connection warm, so a
// process that has served thousands of tenants does not hold thousands of
// connections.
func TestOrgPoolIsSmall(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("SQLite is pinned to one connection by Single; the pool shape is hanzo-sql's")
		}
		db := open(namespace.MustOrg("acme"), "widget")
		var wg sync.WaitGroup
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var one int
				_ = db.QueryRow(`SELECT 1`).Scan(&one)
			}()
		}
		wg.Wait()
		st := db.Stats()
		if st.MaxOpenConnections != 4 || st.OpenConnections > 4 || st.Idle > 1 {
			t.Fatalf("pool = %+v; want at most 4 open and 1 idle", st)
		}
	})
}

// The registry names every store the seam made, so a sweep can enumerate them, and
// asking whether one exists makes nothing.
func TestRegistryNamesTheStores(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("the registry is hanzo-sql's")
		}
		acme, globex := namespace.MustOrg("acme"), namespace.MustOrg("globex")
		if ok, err := locus.Exists(acme, "widget"); err != nil || ok {
			t.Fatalf("a store nothing opened exists: %v, %v", ok, err)
		}
		open(acme, "widget")
		open(globex, "widget")
		open(namespace.System(), "widget")
		open(acme, "gadget")

		if ok, err := locus.Exists(acme, "widget"); err != nil || !ok {
			t.Fatalf("an opened store is not registered: %v, %v", ok, err)
		}
		if ok, _ := locus.Exists(globex, "gadget"); ok {
			t.Fatal("Exists made, or found, a store nobody opened")
		}
		names, err := locus.Stores("widget")
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(names)
		want := []string{acme.String(), globex.String(), namespace.System().String()}
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Fatalf("Stores(widget) = %v, want %v", names, want)
		}
		subs, err := locus.Subsystems(acme)
		if err != nil || !slices.Equal(subs, []string{"gadget", "widget"}) {
			t.Fatalf("Subsystems(acme) = %v, %v; want [gadget widget]", subs, err)
		}
		if subs, err := locus.Subsystems(namespace.MustOrg("initech")); err != nil || len(subs) != 0 {
			t.Fatalf("Subsystems of an entity with no store = %v, %v", subs, err)
		}
	})
}

// Replicas starting together open the same stores at once, and each runs its
// migration. The schema and its registry row are made once, and a migration run under
// dialect.Migrate does not race: without the lock this test fails with a duplicate
// key in pg_type, which is CREATE TABLE IF NOT EXISTS racing itself.
func TestOrgOpensRaceFree(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("the race is hanzo-sql's")
		}
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		for i := range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ns := namespace.MustOrg([]string{"a1", "b2", "c3", "d4"}[i%4])
				db, err := locus.Open(ns, "widget", "")
				if err != nil {
					errs <- err
					return
				}
				defer func() { _ = db.Close() }()
				errs <- dialect.Of(db).Migrate(context.Background(), db, func(tx *sql.Tx) error {
					_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS thing (id BIGINT PRIMARY KEY)`)
					return err
				})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := locus.Stores("widget")
		if err != nil || len(got) != 4 {
			t.Fatalf("Stores = %v, %v; want the four orgs once each", got, err)
		}
	})
}

// A role that is a superuser reaches every database on the server, so the seam
// refuses to run as one. The test server trusts the postgres role on loopback; a
// server that does not cannot be asked, and the test says so.
func TestOrgRefusesASuperuser(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		dsn := locus.DSN()
		if dsn == "" {
			t.Skip("hanzo-sql only")
		}
		u, _ := url.Parse(dsn)
		u.User = url.User("postgres")
		probe, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = probe.Close() }()
		if err := probe.Ping(); err != nil {
			t.Skipf("the server does not let the test connect as postgres: %v", err)
		}
		t.Setenv(locus.Env, u.String())
		if _, err := locus.Open(namespace.MustOrg("acme"), "widget", ""); err == nil || !strings.Contains(err.Error(), "superuser") {
			t.Fatalf("a superuser role was accepted: %v", err)
		}
	})
}

func TestOrgRefusesWhatIsNotAPostgresURL(t *testing.T) {
	t.Setenv(locus.Env, "mysql://root@localhost/db")
	if _, err := locus.Open(namespace.MustOrg("acme"), "widget", ""); err == nil {
		t.Fatal("a mysql URL was accepted as hanzo-sql")
	}
}

// exiting is how many connections the server may still list beyond a process's budget
// at an instant: ones the process has closed, and so no longer counts, whose backends
// have not finished exiting. The budget bounds what the process holds open.
const exiting = 3

// 400 stores held open at once, each used once, is far more pools than the process
// may have connections. Every use succeeds, because a full budget makes the pools
// let go of the connections they are only keeping warm, and the server never sees
// more than the budget.
func TestManyOpenStoresStayInsideTheBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("the budget is hanzo-sql's")
		}
		stop := sqltest.Peak(t, locus.DSN())
		var dbs []*sql.DB
		for i := range 400 {
			db, err := locus.Open(namespace.MustOrg(fmt.Sprintf("org%03d", i)), "flags", "")
			if err != nil {
				t.Fatalf("store %d: %v", i, err)
			}
			dbs = append(dbs, db)
			var one int
			if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
				t.Fatalf("store %d: %v", i, err)
			}
		}
		high := stop()
		for _, db := range dbs {
			_ = db.Close()
		}
		if high > postgres.Budget()+exiting {
			t.Fatalf("the server saw %d connections from one process with a budget of %d", high, postgres.Budget())
		}
		if high == 0 {
			t.Fatal("the probe saw no connections, so it proved nothing")
		}
		t.Logf("400 stores, budget %d, server peak %d", postgres.Budget(), high)
	})
}

// A caller that finds the budget spent waits for a connection to come back; it is
// not given an error because other tenants were busy.
func TestAFullBudgetMakesACallerWait(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("the budget is hanzo-sql's")
		}
		// The late caller's store exists before the budget is spent: making a schema
		// takes a connection too, and this test is about a query that finds none.
		late, err := locus.Open(namespace.MustOrg("late"), "flags", "")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = late.Close() }()

		// Check out the whole budget: pools of four, each connection held.
		var held []*sql.Conn
		var dbs []*sql.DB
		for i := 0; len(held) < postgres.Budget(); i++ {
			db, err := locus.Open(namespace.MustOrg(fmt.Sprintf("holder%02d", i)), "flags", "")
			if err != nil {
				t.Fatal(err)
			}
			dbs = append(dbs, db)
			for range 4 {
				if len(held) == postgres.Budget() {
					break
				}
				c, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, c)
			}
		}

		result := make(chan error, 1)
		began := time.Now()
		go func() {
			var one int
			result <- late.QueryRow(`SELECT 1`).Scan(&one)
		}()
		select {
		case err := <-result:
			t.Fatalf("a caller got through a spent budget: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		_ = held[0].Close() // a tenant finishes; its connection goes back to its pool, idle
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("the waiting caller was refused when a connection came back: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the waiting caller was never served")
		}
		if waited := time.Since(began); waited < 250*time.Millisecond {
			t.Fatalf("served after %v, while the budget was spent", waited)
		}
		for _, c := range held[1:] {
			_ = c.Close()
		}
		for _, db := range dbs {
			_ = db.Close()
		}
	})
}

func TestAServerOffThisHostNeedsTLS(t *testing.T) {
	t.Setenv("CLOUD_ENV", "test")
	for _, dsn := range []string{
		"postgres://u@db.example.com:5432/x",
		"postgres://u@db.example.com:5432/x?sslmode=disable",
		"postgres://u@db.example.com:5432/x?sslmode=prefer",
		"postgres://u@10.0.0.5:5432/x?sslmode=allow",
	} {
		t.Setenv(locus.Env, dsn)
		_, err := locus.Open(namespace.MustOrg("acme"), "flags", "")
		if err == nil || !strings.Contains(err.Error(), "sslmode=require") || !strings.Contains(err.Error(), "Turn ssl on at that server") {
			t.Errorf("%s: %v; want a refusal naming sslmode=require and what to do at the server", dsn, err)
		}
	}
	// Reaching a loopback server in the clear is allowed: it gets as far as dialling.
	t.Setenv(locus.Env, "postgres://u@127.0.0.1:1/x?sslmode=disable")
	if _, err := locus.Open(namespace.MustOrg("acme"), "flags", ""); err == nil || strings.Contains(err.Error(), "without sslmode") {
		t.Errorf("a loopback server was refused for its sslmode: %v", err)
	}
}

// During an outage a caller is answered from the last failure, not by a fresh dial of
// its own, so the callers do not queue behind one another's timeouts.
func TestAnOutageIsRememberedBriefly(t *testing.T) {
	t.Setenv("CLOUD_ENV", "test")
	t.Setenv(locus.Env, "postgres://u@127.0.0.1:1/x?sslmode=disable")
	_, first := locus.Open(namespace.MustOrg("acme"), "flags", "")
	_, second := locus.Open(namespace.MustOrg("globex"), "flags", "")
	if first == nil || first != second {
		t.Fatalf("the second caller was not answered from the first's failure:\n%v\n%v", first, second)
	}
}

// A database belongs to the first deployment that opens it. Another brand or
// environment, copied manifest and all, is refused before it touches a row.
func TestADatabaseBelongsToOneDeployment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		dsn := locus.DSN()
		if dsn == "" {
			t.Skip("hanzo-sql only")
		}
		t.Setenv("CLOUD_ENV", "mainnet")
		db, err := locus.Open(namespace.MustOrg("acme"), "flags", "")
		if err != nil {
			t.Fatalf("the first deployment was refused: %v", err)
		}
		_ = db.Close()

		// The same database, as a process that has not proven it yet: a distinct DSN
		// string, so a distinct entry.
		again := dsn + "&application_name=second"
		t.Setenv(locus.Env, again)
		if db, err := locus.Open(namespace.MustOrg("acme"), "flags", ""); err != nil {
			t.Fatalf("the same deployment was refused on a second start: %v", err)
		} else {
			_ = db.Close()
		}

		t.Setenv("CLOUD_ENV", "testnet")
		t.Setenv(locus.Env, dsn+"&application_name=third")
		if _, err := locus.Open(namespace.MustOrg("acme"), "flags", ""); err == nil || !strings.Contains(err.Error(), "belongs to deployment") {
			t.Fatalf("another deployment was admitted to mainnet's database: %v", err)
		}
	})
}

// At most one holder has a store's lease; it is renewable by that holder, taken
// over when it lapses, and handed on when its holder lets go.
func TestALeaseHasOneHolder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ns := namespace.MustOrg("acme")
		lease := func(holder string, ttl time.Duration) bool {
			t.Helper()
			ok, _, err := locus.Lease(ns, "flags", holder, ttl)
			if err != nil {
				t.Fatal(err)
			}
			return ok
		}
		if !lease("a", time.Minute) {
			t.Fatal("the first holder was refused")
		}
		if lease("b", time.Minute) {
			t.Fatal("two holders have one store's lease")
		}
		if !lease("a", time.Minute) {
			t.Fatal("the holder could not renew")
		}
		if err := locus.Unlease("a"); err != nil {
			t.Fatal(err)
		}
		if !lease("b", time.Second) {
			t.Fatal("a released lease was not handed on")
		}
		if lease("a", time.Minute) {
			t.Fatal("a lease was taken from a holder whose term had not ended")
		}
		time.Sleep(2100 * time.Millisecond)
		if !lease("a", time.Minute) {
			t.Fatal("a lapsed lease was not taken over")
		}
	})
}

// A database bound to no named environment is bound to every copy of the manifest, so
// a process with no CLOUD_ENV does not open one.
func TestNoEnvironmentNoBinding(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		t.Setenv("CLOUD_ENV", "")
		t.Setenv(locus.Env, locus.DSN()+"&application_name=unnamed")
		if _, err := locus.Open(namespace.MustOrg("acme"), "flags", ""); err == nil || !strings.Contains(err.Error(), "CLOUD_ENV") {
			t.Fatalf("a process with no environment opened a database: %v", err)
		}
	})
}

// 200 stores open for the first time at once, with 20 of the budget's 50 held by
// live traffic. Each first open is a migration, and a migration needs ONE connection:
// it runs on the one that holds the store's lock. Needing two (the lock on one, the
// DDL on another) left every opener holding a slot and waiting for a second, with the
// budget spent on the first of each, and 120 of 200 failed after 20 seconds.
func TestAStormOfFirstOpensNeedsOneSlotEach(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("the budget is hanzo-sql's")
		}
		const traffic, stores = 20, 200
		// Live traffic: connections checked out, and kept so for the whole storm.
		var live []*sql.Conn
		var liveDBs []*sql.DB
		for i := 0; len(live) < traffic; i++ {
			db, err := locus.Open(namespace.MustOrg(fmt.Sprintf("live%02d", i)), "flags", "")
			if err != nil {
				t.Fatal(err)
			}
			liveDBs = append(liveDBs, db)
			for range 4 {
				if len(live) == traffic {
					break
				}
				c, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				live = append(live, c)
			}
		}
		defer func() {
			for _, c := range live {
				_ = c.Close()
			}
			for _, db := range liveDBs {
				_ = db.Close()
			}
		}()

		stop := sqltest.Peak(t, locus.DSN())
		began := time.Now()
		var mu sync.Mutex
		var dbs []*sql.DB
		failures := make(chan error, stores)
		var wg sync.WaitGroup
		for i := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				db, err := locus.Open(namespace.MustOrg(fmt.Sprintf("first%03d", i)), "flags", "")
				if err != nil {
					failures <- err
					return
				}
				mu.Lock()
				dbs = append(dbs, db)
				mu.Unlock()
				err = dialect.Of(db).Migrate(context.Background(), db, func(tx *sql.Tx) error {
					_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS thing (id BIGINT PRIMARY KEY, v TEXT)`)
					return err
				})
				if err == nil {
					_, err = db.Exec(`INSERT INTO thing (id, v) VALUES (1, 'x')`)
				}
				if err != nil {
					failures <- err
				}
			}()
		}
		wg.Wait()
		close(failures)
		high := stop()
		for _, db := range dbs {
			_ = db.Close()
		}
		n := 0
		for err := range failures {
			n++
			if n <= 3 {
				t.Errorf("a first open failed: %v", err)
			}
		}
		if n != 0 {
			t.Fatalf("%d of %d concurrent first opens failed with %d of the budget's %d slots held", n, stores, traffic, postgres.Budget())
		}
		if elapsed := time.Since(began); elapsed > 15*time.Second {
			t.Fatalf("the storm took %v; it should not be waiting on the budget's bound", elapsed)
		}
		if high > postgres.Budget()+exiting {
			t.Fatalf("the server saw %d connections with a budget of %d", high, postgres.Budget())
		}
		t.Logf("%d first opens beside %d live connections, budget %d, server peak %d, %v", stores, traffic, postgres.Budget(), high, time.Since(began).Round(time.Millisecond))
	})
}

// A replica that does not own a store asks about it, and asking must not write. An
// INSERT ... ON CONFLICT takes the row lock even when the conflict refuses it, which
// stamps the row's xmax and assigns the asking transaction an id; the denied ask is a
// plain SELECT. Two properties are held, neither of which reads a counter the rest of
// the server moves:
//
//   - the lease row's own version stamps (xmin, xmax, ctid, holder, expires) are the
//     same after the asks, which no other session touches;
//   - the asking backend's own WAL counter is the same after the asks. A transaction
//     that is assigned an id writes at least its commit record, a read-only one writes
//     none, and the counter is that one backend's, so a neighbour's transactions and
//     autovacuum are not in it.
func TestADeniedLeaseAskWritesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ns := namespace.MustOrg("acme")
		if ok, _, err := locus.Lease(ns, "flags", "owner", time.Minute); err != nil || !ok {
			t.Fatalf("the owner: %v %v", ok, err)
		}
		schema, err := postgres.Schema(ns, "flags")
		if err != nil {
			t.Fatal(err)
		}
		admin, err := postgres.Admin(locus.DSN())
		if err != nil {
			t.Fatal(err)
		}
		// The asks run on the one admin connection; backend reads its counters, and
		// forces them out of the backend first, which otherwise publishes them lazily.
		backend := func() (pid int, wal int64) {
			c, err := admin.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if _, err := c.ExecContext(context.Background(), `SELECT pg_stat_force_next_flush()`); err != nil {
				t.Fatal(err)
			}
			if err := c.QueryRowContext(context.Background(), `SELECT pg_backend_pid(), wal_records FROM pg_stat_get_backend_wal(pg_backend_pid())`).Scan(&pid, &wal); err != nil {
				t.Fatal(err)
			}
			return pid, wal
		}
		probe, err := sql.Open("pgx", locus.DSN())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = probe.Close() }()
		// xmin is the transaction that wrote the row, xmax the one that locked or
		// replaced it (0 while none has), ctid where this version lives.
		stamp := func() string {
			var s string
			err := probe.QueryRow(`SELECT xmin::text || '/' || xmax::text || '/' || ctid::text || '/' || holder || '/' || expires::text FROM public.cloud_leases WHERE schema = $1`, schema).Scan(&s)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		before := stamp()
		if parts := strings.Split(before, "/"); parts[1] != "0" {
			t.Fatalf("the owner's row is already locked or replaced: %s", before)
		}
		pid, walBefore := backend()
		for range 200 {
			ok, retry, err := locus.Lease(ns, "flags", "waiting", time.Minute)
			if err != nil || ok || retry <= 0 {
				t.Fatalf("a lease held by another was %v (retry %v, %v)", ok, retry, err)
			}
		}
		pidAfter, walAfter := backend()
		if pidAfter != pid {
			t.Fatalf("the asks did not stay on backend %d: now %d", pid, pidAfter)
		}
		if walAfter != walBefore {
			t.Errorf("200 denied asks assigned transaction ids: backend %d wrote %d WAL records", pid, walAfter-walBefore)
		}
		if after := stamp(); after != before {
			t.Errorf("200 denied asks wrote to the lease row: xmin/xmax/ctid/holder/expires was %s, now %s", before, after)
		}
	})
}

// The observer is told the text of every statement a connection is asked to run,
// whichever way it is asked: executed, queried, or prepared.
func TestTheObserverSeesEveryStatement(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		db := open(namespace.MustOrg("acme"), "flags")
		var mu sync.Mutex
		var seen []string
		restore := postgres.Observe(func(q string) {
			mu.Lock()
			seen = append(seen, q)
			mu.Unlock()
		})
		if _, err := db.Exec(`SELECT 11`); err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(`SELECT 12`)
		if err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		stmt, err := db.Prepare(`SELECT 13`)
		if err != nil {
			t.Fatal(err)
		}
		_ = stmt.Close()
		restore()
		if _, err := db.Exec(`SELECT 14`); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		got := strings.Join(seen, "|")
		for _, want := range []string{"SELECT 11", "SELECT 12", "SELECT 13"} {
			if !strings.Contains(got, want) {
				t.Errorf("the observer missed %q; saw %q", want, got)
			}
		}
		if strings.Contains(got, "SELECT 14") {
			t.Errorf("the observer saw a statement after it was removed: %q", got)
		}
	})
}

// Giving leases up is bounded, dialling included: a server that accepts and never
// answers must not hold a shutdown for the dial's bound as well as the statement's.
func TestUnleaseIsBoundedAgainstASilentServer(t *testing.T) {
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
			defer func() { _ = c.Close() }()
		}
	}()
	t.Setenv(locus.Env, fmt.Sprintf("postgres://u@%s/x?sslmode=disable", ln.Addr()))
	t.Setenv("CLOUD_ENV", "test")
	began := time.Now()
	if err := locus.Unlease("nobody"); err == nil {
		t.Fatal("Unlease against a silent server answered nil")
	}
	if took := time.Since(began); took > 2500*time.Millisecond {
		t.Fatalf("Unlease took %v; its bound is 2s", took)
	}
}

// Leases of holders that are gone are deleted a day after they lapse, when a process
// proves the server.
func TestLapsedLeasesArePruned(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		dsn := locus.DSN()
		if dsn == "" {
			t.Skip("hanzo-sql only")
		}
		ns := namespace.MustOrg("acme")
		for _, holder := range []string{"gone", "recent"} {
			if ok, _, err := locus.Lease(ns, "flags"+holder, holder, time.Minute); err != nil || !ok {
				t.Fatalf("lease for %s: %v %v", holder, ok, err)
			}
		}
		probe, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = probe.Close() }()
		if _, err := probe.Exec(`UPDATE public.cloud_leases SET expires = extract(epoch FROM now())::bigint - 90000 WHERE holder = 'gone'`); err != nil {
			t.Fatal(err)
		}
		if _, err := probe.Exec(`UPDATE public.cloud_leases SET expires = extract(epoch FROM now())::bigint - 100 WHERE holder = 'recent'`); err != nil {
			t.Fatal(err)
		}
		t.Setenv(locus.Env, dsn+"&application_name=restarted")
		if db, err := locus.Open(ns, "flags", ""); err != nil {
			t.Fatal(err)
		} else {
			_ = db.Close()
		}
		var gone, recent int
		_ = probe.QueryRow(`SELECT COUNT(*) FROM public.cloud_leases WHERE holder = 'gone'`).Scan(&gone)
		_ = probe.QueryRow(`SELECT COUNT(*) FROM public.cloud_leases WHERE holder = 'recent'`).Scan(&recent)
		if gone != 0 || recent != 1 {
			t.Fatalf("after a restart: %d rows for the holder gone a day ago (want 0), %d for the one gone minutes ago (want 1)", gone, recent)
		}
	})
}
