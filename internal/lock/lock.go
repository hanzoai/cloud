package lock

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
)

// Keyed holds one mutex per key while anyone holds or waits for it. The zero
// value is ready to use.
type Keyed struct {
	mu sync.Mutex
	m  map[string]*held
	// slots admits the keys Across holds against a database, at most most at once.
	slots chan struct{}
}

// most is how many keys one Keyed holds against a database at a time. Each is a
// connection of the store's pool, held from the grant to the release, and the pool
// is four: with two held, the work under each can still be served a connection of
// its own, and a burst of keys queues here holding none rather than spending the pool
// its holders need to finish.
const most = 2

type held struct {
	ch chan struct{} // holds one token while the key is locked
	n  int           // holders and waiters
}

// Hold blocks until the caller holds key and answers the release.
func (k *Keyed) Hold(key string) (release func()) {
	release, _ = k.Take(context.Background(), key)
	return release
}

// Take is Hold that gives up when ctx ends, and answers why. The release may be
// called again.
func (k *Keyed) Take(ctx context.Context, key string) (release func(), err error) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*held{}
	}
	h := k.m[key]
	if h == nil {
		h = &held{ch: make(chan struct{}, 1)}
		k.m[key] = h
	}
	h.n++
	k.mu.Unlock()
	drop := func() {
		k.mu.Lock()
		if h.n--; h.n == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
	select {
	case h.ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-h.ch; drop() }) }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// Refs is how many callers hold or wait for key in this process.
func (k *Keyed) Refs(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	if h := k.m[key]; h != nil {
		return h.n
	}
	return 0
}

// shared is the database a lock is held on: db, or, for a caller with no store of its
// own (db nil), the process's lock pool (locus.Locks), which is nil where there is
// no hanzo-sql and so one process.
func shared(db *sql.DB) (*sql.DB, error) {
	if db != nil {
		return db, nil
	}
	return locus.Locks()
}

// Across holds key against every replica that reaches db's database, until release
// is called: one holder at a time across all of them, and other keys do not wait.
// On PostgreSQL the lock is an advisory lock held by a transaction on a connection
// of db (Dialect.Serialize through dialect.Held), so a replica that dies while
// holding it drops the connection and frees the key. ctx bounds the wait; once the
// key is held nothing but release ends it.
//
// The key is db's own: it names a thing in that store, and the same key on another
// store is another lock. Two callers that guard one thing name one key and one db's
// database. A caller with no store passes nil, and holds the key on the process's lock
// pool (locus.Locks): there the key belongs to the database, whatever the work under
// it touches, so it names the thing guarded as a whole (`filing:<org>`).
//
// THE CONNECTION IS THE LOCK. It is held from the grant to the release and the guarded
// work must not need a second of db's pool to make progress, which a pool of four
// whose holders each wait for one cannot give: work that reads or writes this store
// does so on the transaction Dialect.Serialize returns, or holds few enough keys at a
// time that the pool is never spent. SQLite is one process with one connection, and
// there is nothing to take: the caller's Keyed is its lock, and Keyed.Across is both.
func Across(ctx context.Context, db *sql.DB, key string) (release func(), err error) {
	if db, err = shared(db); err != nil {
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	if db == nil {
		return func() {}, nil
	}
	d := dialect.Of(db)
	if !d.Postgres() {
		return func() {}, nil
	}
	_, held, err := dialect.Held(ctx, 0, func(ctx context.Context) (*sql.Tx, error) {
		return d.Serialize(ctx, db, key)
	})
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	return func() { _ = held() }, nil
}

// Across holds key in this process and then against every replica that reaches db's
// database. A waiter in this process queues here, holding no connection, so a burst
// for one key spends one connection and not one each, and a Keyed holds at most two
// keys against a database at once, which leaves the store's pool the connections the
// work under them needs.
func (k *Keyed) Across(ctx context.Context, db *sql.DB, key string) (release func(), err error) {
	local, err := k.Take(ctx, key)
	if err != nil {
		return nil, err
	}
	if db, err = shared(db); err != nil {
		local()
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	if db == nil || !dialect.Of(db).Postgres() {
		return local, nil
	}
	k.mu.Lock()
	if k.slots == nil {
		k.slots = make(chan struct{}, most)
	}
	slots := k.slots
	k.mu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		local()
		return nil, ctx.Err()
	}
	shared, err := Across(ctx, db, key)
	if err != nil {
		<-slots
		local()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { shared(); <-slots; local() }) }, nil
}
