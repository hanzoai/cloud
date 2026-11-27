package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// needServer is the postgres these tests run against. Unset, it skips on a
// workstation and fails in CI, as sqltest does (this package's own tests cannot import
// it: it imports this one).
func needServer(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CLOUD_TEST_SQL_DSN")
	if dsn != "" {
		return dsn
	}
	if kit.CI() {
		t.Fatal("CLOUD_TEST_SQL_DSN is not set in CI, so this was not run on PostgreSQL; run the gate under scripts/postgres.sh")
	}
	t.Skip("CLOUD_TEST_SQL_DSN is not set; scripts/postgres.sh starts a server for a command")
	return ""
}

// The budget hands out its slots, makes a caller that finds none wait, and gives up
// after the bound with an error that says what to raise.
func TestLimiterWaitsThenGivesUp(t *testing.T) {
	defer func(w time.Duration) { capWait = w }(capWait)
	capWait = 300 * time.Millisecond

	l := newLimiter(2)
	ctx := context.Background()
	for range 2 {
		if err := l.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// A third waits, and proceeds when a slot is given back.
	freed := time.AfterFunc(100*time.Millisecond, l.release)
	defer freed.Stop()
	began := time.Now()
	if err := l.acquire(ctx); err != nil {
		t.Fatalf("a caller that could have had a slot after 100ms was refused: %v", err)
	}
	if waited := time.Since(began); waited < 80*time.Millisecond {
		t.Fatalf("the third caller got a slot after %v, before one was free", waited)
	}

	// With nothing freed it waits the bound and then errors.
	began = time.Now()
	err := l.acquire(ctx)
	if err == nil || !strings.Contains(err.Error(), ConnsEnv) {
		t.Fatalf("an exhausted budget answered %v; want an error naming %s", err, ConnsEnv)
	}
	if waited := time.Since(began); waited < 250*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("gave up after %v; the bound is %v", waited, capWait)
	}

	// A caller whose context ends stops waiting at once.
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := l.acquire(cctx); err == nil {
		t.Fatal("a cancelled caller was given a slot")
	}
}

// A connection that resolves tables to the wrong schema is refused at dial, and the
// slot it took is given back. The test server's role has no schema of its own, so
// its tables resolve to public.
func TestConnectorChecksTheSchema(t *testing.T) {
	cfg, err := pgx.ParseConfig(needServer(t))
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(3)
	before := len(l.slots)

	right := connector{inner: stdlib.GetConnector(*cfg), lim: l, schema: "public"}
	c, err := right.Connect(context.Background())
	if err != nil {
		t.Fatalf("a connection on the schema it was told to expect was refused: %v", err)
	}
	if len(l.slots) != before+1 {
		t.Fatalf("an open connection holds %d slots, want 1", len(l.slots)-before)
	}
	_ = c.Close()
	_ = c.Close() // closing twice gives the slot back once
	if len(l.slots) != before {
		t.Fatalf("a closed connection still holds %d slots", len(l.slots)-before)
	}

	wrong := connector{inner: stdlib.GetConnector(*cfg), lim: l, schema: "s_somebody_elses"}
	if _, err := wrong.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "s_somebody_elses") {
		t.Fatalf("a connection on the wrong schema was accepted: %v", err)
	}
	if len(l.slots) != before {
		t.Fatalf("a refused connection kept %d slots", len(l.slots)-before)
	}
}

// A waiter makes room by closing idle connections, the OLDEST first and no more than it
// needs. Five stores each keep a warm connection, two are checked out, and a sixth
// store asks for one of a budget of seven: exactly the oldest store's goes.
func TestAWaiterClosesOnlyAsManyIdleConnectionsAsItNeeds(t *testing.T) {
	cfg, err := pgx.ParseConfig(needServer(t))
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(7)
	mk := func() *sql.DB {
		db := sql.OpenDB(connector{inner: stdlib.GetConnector(*cfg), lim: l})
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(1)
		return db
	}
	ctx := context.Background()
	var warm []*sql.DB
	for range 5 { // oldest first: each is used, and left idle, a little after the one before
		db := mk()
		var one int
		if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
			t.Fatal(err)
		}
		warm = append(warm, db)
		time.Sleep(2 * minIdle)
	}
	busy := mk()
	var held []*sql.Conn
	for range 2 {
		c, err := busy.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	if len(l.slots) != 7 {
		t.Fatalf("setup holds %d of 7 slots", len(l.slots))
	}
	oldest := func() *slotConn { // the connection the oldest store keeps warm
		l.mu.Lock()
		defer l.mu.Unlock()
		var first *slotConn
		var firstSince time.Time
		for c := range l.conns {
			if since, ok := c.idleSince(); ok && (first == nil || since.Before(firstSince)) {
				first, firstSince = c, since
			}
		}
		return first
	}()

	late := mk()
	var one int
	if err := late.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the sixth store was not served: %v", err)
	}
	if got := l.closed.Load(); got != 1 {
		t.Fatalf("%d idle connections were closed to make room for one waiter", got)
	}
	if _, alive := oldest.idleSince(); alive {
		t.Fatal("the oldest idle connection survived, so a younger one was closed instead")
	}
	idle := 0
	for c := range l.conns {
		c.mu.Lock()
		if !c.since.IsZero() && !c.dead { // idle, however recently
			idle++
		}
		c.mu.Unlock()
	}
	// four warm stores remain asleep, and the late one's connection is idle again too
	if idle != 5 {
		t.Fatalf("%d idle connections remain; the four younger stores' and the late store's, 5, were to be left alone", idle)
	}
	// The store whose connection was closed is not left broken: its next query dials.
	if err := warm[0].QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the store whose idle connection was closed under it failed on its next query: %v", err)
	}
	for _, c := range held {
		_ = c.Close()
	}
	for _, db := range append(warm, busy, late) {
		_ = db.Close()
	}
	if len(l.slots) != 0 || len(l.conns) != 0 {
		t.Fatalf("after closing everything: %d slots held, %d connections tracked", len(l.slots), len(l.conns))
	}
}

// Stores that query more often than minIdle never have a connection old enough to
// close, so the floor that protects them also locked a newcomer out for the whole
// bound. 16 stores share a budget of 8, think for 0, 5 or 30 ms between queries, and
// every one of them must complete a query, not only the ones that got in first. The
// run goes on until each has, and the wait of the slowest is held far under the bound
// a starved store would have waited out (capWait, 20 s).
func TestEveryStoreMakesProgressUnderContention(t *testing.T) {
	defer func(d time.Duration) { starveAfter = d }(starveAfter)
	starveAfter = 200 * time.Millisecond
	cfg, err := pgx.ParseConfig(needServer(t))
	if err != nil {
		t.Fatal(err)
	}
	const stores = 16
	l := newLimiter(8)
	think := []time.Duration{0, 5 * time.Millisecond, 30 * time.Millisecond}
	var dbs [stores]*sql.DB
	for i := range dbs {
		dbs[i] = sql.OpenDB(connector{inner: stdlib.GetConnector(*cfg), lim: l})
		dbs[i].SetMaxOpenConns(2)
		dbs[i].SetMaxIdleConns(1)
	}
	var queries [stores]atomic.Int64
	var first [stores]atomic.Int64 // nanoseconds from the start to the store's first query
	var failed, progressed atomic.Int64
	began := time.Now()
	deadline := began.Add(15 * time.Second)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) && (progressed.Load() < stores || time.Since(began) < 1500*time.Millisecond) {
				var one int
				if err := dbs[i].QueryRow(`SELECT 1`).Scan(&one); err != nil {
					failed.Add(1)
					return
				}
				if queries[i].Add(1) == 1 {
					first[i].Store(int64(time.Since(began)))
					progressed.Add(1)
				}
				time.Sleep(think[i%len(think)])
			}
		}()
	}
	wg.Wait()
	for _, db := range dbs {
		_ = db.Close()
	}
	if failed.Load() != 0 {
		t.Fatalf("%d stores failed under contention", failed.Load())
	}
	var total int64
	var slowest time.Duration
	for i := range queries {
		total += queries[i].Load()
		if queries[i].Load() == 0 {
			t.Errorf("store %d (think %v) never completed a query: it was starved", i, think[i%len(think)])
			continue
		}
		slowest = max(slowest, time.Duration(first[i].Load()))
	}
	if slowest > 8*time.Second {
		t.Errorf("the slowest store waited %v for its first query, against a bound of %v", slowest, capWait)
	}
	t.Logf("16 stores on 8 slots: %d queries, %d idle connections closed, the slowest waited %v for its first", total, l.closed.Load(), slowest.Round(time.Millisecond))
}

// Eight stores that are never idle for 50 ms hold a budget of eight between them, and
// four newcomers each need one query. The first is served once it has waited
// starveAfter, by taking the least recently used connection, and the others as it
// finishes with its own.
func TestNewcomersAreServedWhenEveryStoreIsBusy(t *testing.T) {
	defer func(d time.Duration) { starveAfter = d }(starveAfter)
	starveAfter = 300 * time.Millisecond
	cfg, err := pgx.ParseConfig(needServer(t))
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(8)
	mk := func() *sql.DB {
		db := sql.OpenDB(connector{inner: stdlib.GetConnector(*cfg), lim: l})
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(1)
		return db
	}
	stop := make(chan struct{})
	var busy sync.WaitGroup
	var residents []*sql.DB
	for range 8 { // each queries every 20 ms: its connection is never idle for 50
		db := mk()
		residents = append(residents, db)
		busy.Add(1)
		go func() {
			defer busy.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var one int
				_ = db.QueryRow(`SELECT 1`).Scan(&one)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond) // the residents fill the budget
	if len(l.slots) != 8 {
		t.Fatalf("setup holds %d of 8 slots", len(l.slots))
	}
	var wg sync.WaitGroup
	took := make(chan time.Duration, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db := mk()
			defer func() { _ = db.Close() }()
			began := time.Now()
			var one int
			if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
				errs <- err
				return
			}
			took <- time.Since(began)
		}()
	}
	wg.Wait()
	close(stop)
	busy.Wait()
	for _, db := range residents {
		_ = db.Close()
	}
	close(errs)
	close(took)
	for err := range errs {
		t.Fatalf("a newcomer was not served: %v", err)
	}
	for d := range took {
		if d > 5*time.Second {
			t.Fatalf("a newcomer waited %v with the bound at %v", d, capWait)
		}
	}
	// One forced close is enough to start them: each newcomer closes its pool when its
	// query is done, and the slot it held goes to the next.
	if l.closed.Load() < 1 {
		t.Fatal("no idle connection was closed, so the newcomers got in some other way than the test means")
	}
}

// A server that accepts a connection and never speaks must not hold every caller for
// the dial's whole timeout, each in turn. The callers who arrive while a dial is in
// flight are answered with the last failure at once; one caller looks again.
func TestAnUnresponsiveServerDoesNotQueueCallers(t *testing.T) {
	defer func(d, f time.Duration) { dialTimeout, failureMemory = d, f }(dialTimeout, failureMemory)
	dialTimeout, failureMemory = 400*time.Millisecond, 600*time.Millisecond
	t.Setenv("CLOUD_ENV", "test")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // held, never answered
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	}()
	dsn := fmt.Sprintf("postgres://u@%s/x?sslmode=disable", ln.Addr())

	wave := func(n int) (slowest time.Duration, instant int) {
		var wg sync.WaitGroup
		var m sync.Mutex
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				began := time.Now()
				_, err := serverOf(dsn)
				took := time.Since(began)
				if err == nil {
					t.Error("an unresponsive server was proven")
				}
				m.Lock()
				slowest = max(slowest, took)
				if took < 50*time.Millisecond {
					instant++
				}
				m.Unlock()
			}()
		}
		wg.Wait()
		return
	}

	// First wave: nobody has a history, so they wait for the one dial, once.
	slowest, _ := wave(20)
	if slowest > dialTimeout+500*time.Millisecond {
		t.Fatalf("20 callers took %v; one dial takes %v", slowest, dialTimeout)
	}
	// Second wave, inside the failure's memory: all answered at once.
	if _, instant := wave(20); instant != 20 {
		t.Fatalf("only %d of 20 callers were answered from memory", instant)
	}
	// Third wave, after it: one caller looks again; the rest are told the last failure
	// without waiting for that look.
	time.Sleep(failureMemory + 50*time.Millisecond)
	slowest, instant := wave(20)
	if instant < 19 {
		t.Fatalf("%d of 20 callers were answered at once while one refreshed; want 19", instant)
	}
	if slowest > dialTimeout+500*time.Millisecond {
		t.Fatalf("the refresh took %v", slowest)
	}
}
