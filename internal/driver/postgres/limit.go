package postgres

// limit.go — ONE budget of server connections for the whole process.
//
// Every pool in postgres.go is small, but there is one per store, and a process
// serves thousands of stores: the sum is what hanzo-sql has to carry, shared with
// every other service, against its max_connections. So the budget is not per pool.
// A single limiter is taken by every connection any pool dials and given back when
// that connection closes. The default is 50 per process, which three replicas
// carry as 150 of the server's 500; CLOUD_SQL_CONNS overrides it.
//
// A pool that cannot dial WAITS, up to a bound, rather than failing: a burst that
// needs the 51st connection is a delay, and a request does not become an error
// because other tenants were busy a moment ago.
//
// What a waiter waits for is mostly connections that are asleep. Pools keep one warm,
// and a warm connection costs a slot, so a spent budget is usually a budget of idle
// connections. The limiter therefore knows which of its connections are idle and
// since when (database/sql tells the driver when it takes a connection back and when
// it hands one out), and a waiter makes room by closing the idle ones, OLDEST first
// and no more of them than there are waiters. Closing every idle connection whenever
// anyone waited made every pool redial at once, and the redials are what the budget
// was spent on: throughput fell by four fifths.

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/hanzoai/cloud/sdk/env"
)

// ConnsEnv names the process's connection budget.
const ConnsEnv = "CLOUD_SQL_CONNS"

// defaultConns is the budget when ConnsEnv is unset.
const defaultConns = 50

// Budget is the number of server connections this process may hold at once.
func Budget() int { return cap(limit().slots) }

// capWait bounds how long a dial waits for a slot. A variable so this package's own
// tests can shorten it.
var capWait = 20 * time.Second

// reclaimEvery is how often a waiter looks for idle connections to close.
const reclaimEvery = 50 * time.Millisecond

// minIdle is how long a connection must have been idle before an ordinary waiter may
// close it to make room. database/sql hands a connection a caller has just given back
// straight to a caller that is waiting for one, and it does that after the driver has
// been told the connection is idle: a connection closed inside that gap reaches the
// waiter dead, in the one attempt that cannot be retried. A connection that has been
// idle this long is sitting in the pool and not on its way to anyone.
const minIdle = 50 * time.Millisecond

// settle is how long the slot of a connection closed to make room is held after it is
// closed.
const settle = 15 * time.Millisecond

// starveAfter is how long a waiter may be refused before it takes the least recently
// used idle connection whatever its age. The floor above protects a store that is
// busy, and so protects it for ever: stores that query more often than minIdle never
// have a connection that old, and a newcomer then waits out the whole bound. Past a
// second the newcomer has waited long enough to be owed one, and the cost of taking
// it is the cost any closed idle connection has: database/sql retries a connection
// that turns out dead, up to three times. A variable so this package's own tests can
// shorten it.
var starveAfter = time.Second

type limiter struct {
	slots      chan struct{}
	moves      chan struct{} // bounds concurrent Move calls so lockMove cannot consume all slots
	waiters    atomic.Int64
	reclaiming atomic.Bool
	closed     atomic.Int64 // idle connections closed to make room, for tests and for knowing
	settling   atomic.Int64 // closed connections whose slots are not yet given back

	mu    sync.Mutex
	conns map[*slotConn]struct{}
}

var (
	limitOnce sync.Once
	budget    *limiter
)

// limit is the process's limiter, sized on first use.
func limit() *limiter {
	limitOnce.Do(func() { budget = newLimiter(env.Int(ConnsEnv, defaultConns)) })
	return budget
}

func newLimiter(n int) *limiter {
	return &limiter{
		slots: make(chan struct{}, n),
		moves: make(chan struct{}, max(1, n/4)),
		conns: map[*slotConn]struct{}{},
	}
}

// acquire takes a slot, waiting for one if the budget is spent.
func (l *limiter) acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	default:
	}
	l.waiters.Add(1)
	defer l.waiters.Add(-1)
	began := time.Now()
	deadline := time.NewTimer(capWait)
	defer deadline.Stop()
	tick := time.NewTicker(reclaimEvery)
	defer tick.Stop()
	for {
		l.reclaim(time.Since(began) >= starveAfter)
		select {
		case l.slots <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("postgres: all %d of this process's hanzo-sql connections were in use for %v; raise %s if the server has room", cap(l.slots), capWait, ConnsEnv)
		case <-tick.C:
		}
	}
}

func (l *limiter) release() { <-l.slots }

// reclaim closes idle connections, oldest first, as many as there are waiters beyond
// the slots already free, and only ones idle for minIdle. A waiter that has been
// starving also takes one younger than that, the least recently used: one a call, so
// a starving newcomer costs a busy store a single connection and not its pool. One
// waiter scans at a time.
func (l *limiter) reclaim(starving bool) {
	if !l.reclaiming.CompareAndSwap(false, true) {
		return
	}
	defer l.reclaiming.Store(false)
	need := int(l.waiters.Load()) - (cap(l.slots) - len(l.slots)) - int(l.settling.Load())
	if starving {
		need = max(need, 1)
	}
	if need <= 0 {
		return
	}
	l.mu.Lock()
	all := make([]*slotConn, 0, len(l.conns))
	for c := range l.conns {
		all = append(all, c)
	}
	l.mu.Unlock()

	type idle struct {
		c     *slotConn
		since time.Time
	}
	var asleep []idle
	for _, c := range all {
		if since, ok := c.idleSince(); ok {
			asleep = append(asleep, idle{c, since})
		}
	}
	sort.Slice(asleep, func(i, j int) bool { return asleep[i].since.Before(asleep[j].since) })
	forced := false
	for _, a := range asleep {
		if need == 0 {
			return
		}
		switch {
		case time.Since(a.since) >= minIdle:
			if a.c.kill(false) {
				l.closed.Add(1)
				need--
			}
		case starving && !forced:
			forced = true // the oldest of the young: the rest are younger still
			if a.c.kill(true) {
				l.closed.Add(1)
				need--
			}
		}
	}
}

func (l *limiter) track(c *slotConn) {
	l.mu.Lock()
	l.conns[c] = struct{}{}
	l.mu.Unlock()
}

func (l *limiter) untrack(c *slotConn) {
	l.mu.Lock()
	delete(l.conns, c)
	l.mu.Unlock()
}

// connector dials through the budget, and checks that what it dialled is the store.
type connector struct {
	inner driver.Connector
	lim   *limiter
	// schema is the schema every table must resolve to on this pool's connections;
	// empty for the administrative pool, which names none.
	schema string
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	l := c.lim
	if err := l.acquire(ctx); err != nil {
		return nil, err
	}
	dc, err := c.inner.Connect(ctx)
	if err != nil {
		l.release()
		return nil, err
	}
	conn := &slotConn{Conn: dc.(*stdlib.Conn), lim: l}
	l.track(conn)
	if c.schema != "" {
		// search_path is a startup parameter the server could ignore or a proxy could
		// drop, and a connection that resolves tables anywhere else is one tenant's
		// SQL running in another's schema.
		var got string
		if err := conn.Conn.Conn().QueryRow(ctx, `SELECT current_schema()`).Scan(&got); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if got != c.schema {
			_ = conn.Close()
			return nil, fmt.Errorf("postgres: a connection resolves tables to schema %q, not the store's %q", got, c.schema)
		}
	}
	return conn, nil
}

func (c connector) Driver() driver.Driver { return c.inner.Driver() }

// slotConn is a pgx connection that gives its slot back when it closes, and that
// knows whether it is idle. It embeds the pgx connection so every optional interface
// database/sql looks for (CheckNamedValue, Ping, the context-taking Exec and Query)
// is still the pgx one; IsValid and ResetSession are the two it wraps.
type slotConn struct {
	*stdlib.Conn
	lim  *limiter
	once sync.Once

	// shut closes the pgx connection exactly once, whoever asks first. A connection
	// the limiter has closed is later closed again by database/sql, when it finds the
	// dead one in its free list and turns it away; pgx does not tolerate two closes at
	// once, and the race detector says so.
	shut    sync.Once
	shutErr error

	mu    sync.Mutex
	since time.Time // when database/sql took it back; zero while it is in use
	dead  bool
}

// IsValid is asked by database/sql each time it takes the connection back from a
// caller, which is the moment it becomes idle.
func (c *slotConn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return false
	}
	c.since = time.Now()
	return !c.Conn.Conn().IsClosed()
}

// ResetSession is asked by database/sql before it hands an idle connection out again.
// One the limiter has closed answers bad connection, and database/sql discards it and
// dials, without the caller seeing anything.
func (c *slotConn) ResetSession(ctx context.Context) error {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return driver.ErrBadConn
	}
	c.since = time.Time{}
	c.mu.Unlock()
	return c.Conn.ResetSession(ctx)
}

func (c *slotConn) idleSince() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.since, !c.since.IsZero() && !c.dead
}

// kill closes the connection if it is idle, and gives its slot back now. database/sql
// still holds the dead connection in its free list until it next looks at it, when
// ResetSession turns it away; the slot is not held for that long. Unless force is set
// it is closed only after minIdle.
func (c *slotConn) kill(force bool) bool {
	c.mu.Lock()
	if c.since.IsZero() || c.dead || (!force && time.Since(c.since) < minIdle) {
		c.mu.Unlock()
		return false
	}
	c.dead = true
	c.mu.Unlock()
	_ = c.shutdown()
	// The slot comes back a moment later, not now. Closing sends the server a goodbye
	// and drops the socket, and the server's backend takes a few milliseconds to
	// exit; a slot given back at once is dialled again at once, and for those few
	// milliseconds the server lists both. Holding it that long keeps the server's own
	// count, and not only this process's, inside the budget.
	c.lim.settling.Add(1)
	time.AfterFunc(settle, func() {
		c.give()
		c.lim.settling.Add(-1)
	})
	return true
}

func (c *slotConn) Close() error {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()
	err := c.shutdown()
	c.give()
	return err
}

func (c *slotConn) shutdown() error {
	c.shut.Do(func() { c.shutErr = c.Conn.Close() })
	return c.shutErr
}

// give returns the slot, once, however many ways the connection is closed.
func (c *slotConn) give() {
	c.once.Do(func() {
		c.lim.untrack(c)
		c.lim.release()
	})
}

// observer, when set, is told the text of every statement a connection of this process
// is asked to run. It exists for the test harness (internal/sqltest), which holds
// each statement a test executes to the dialect both engines read alike; nothing
// else sets it.
var observer atomic.Pointer[func(query string)]

// Observe installs fn as the observer and returns what restores the one before it.
func Observe(fn func(query string)) (restore func()) {
	old := observer.Swap(&fn)
	return func() { observer.Store(old) }
}

func watch(query string) {
	if f := observer.Load(); f != nil {
		(*f)(query)
	}
}

func (c *slotConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	watch(query)
	return c.Conn.ExecContext(ctx, query, args)
}

func (c *slotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	watch(query)
	return c.Conn.QueryContext(ctx, query, args)
}

func (c *slotConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	watch(query)
	return c.Conn.PrepareContext(ctx, query)
}
