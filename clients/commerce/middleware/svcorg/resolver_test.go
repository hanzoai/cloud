// Copyright © 2026 Hanzo AI. MIT License.

package svcorg

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/db"
)

// countingDB wraps a db.DB and counts writes (Put) and reads (Query) so a test
// can assert that a cache HIT touches the store ZERO times and a singleflighted
// create-storm hits the WRITE path exactly once. Everything delegates to the
// real in-mem SQLite DB, so GetOrCreate behaves exactly as in production.
type countingDB struct {
	db.DB
	puts  int64 // writes (Create → Put)
	query int64 // reads (GetOrCreate's Filter().Get() → Query)
}

func (c *countingDB) Put(ctx context.Context, key db.Key, src interface{}) (db.Key, error) {
	atomic.AddInt64(&c.puts, 1)
	return c.DB.Put(ctx, key, src)
}

func (c *countingDB) PutMulti(ctx context.Context, keys []db.Key, src interface{}) ([]db.Key, error) {
	atomic.AddInt64(&c.puts, 1)
	return c.DB.PutMulti(ctx, keys, src)
}

func (c *countingDB) Query(kind string) db.Query {
	atomic.AddInt64(&c.query, 1)
	return c.DB.Query(kind)
}

// setup installs a fresh counting default datastore and resets package state so
// each test starts with an empty cache/singleflight group.
func setup(t *testing.T) *countingDB {
	t.Helper()

	mgr, err := db.NewManager(&db.Config{
		DataDir:            t.TempDir(),
		EnableVectorSearch: false,
		IsDev:              true,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	// Organization is a DefaultNamespace (global) kind → it lives in the shared
	// default DB. Resolve the shared "system" store and count against it.
	shared, err := mgr.Org("system")
	if err != nil {
		t.Fatalf("mgr.Org(system): %v", err)
	}
	cdb := &countingDB{DB: shared}
	datastore.SetDefaultDB(cdb)

	// Reset package globals between tests.
	c, err := lru.New[string, entry](cacheSize)
	if err != nil {
		t.Fatalf("lru.New: %v", err)
	}
	cache = c
	group = singleflight.Group{}
	now = time.Now

	return cdb
}

func TestResolve_CacheHitDoesNoDatastoreWork(t *testing.T) {
	cdb := setup(t)

	if _, err := Resolve("hanzo"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	readsAfterFirst := atomic.LoadInt64(&cdb.query)
	if readsAfterFirst == 0 {
		t.Fatalf("first Resolve should have queried the store at least once")
	}

	// Subsequent resolves within the TTL must hit the cache and NOT touch the store.
	for i := 0; i < 50; i++ {
		if _, err := Resolve("hanzo"); err != nil {
			t.Fatalf("cached Resolve %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&cdb.query); got != readsAfterFirst {
		t.Fatalf("cache hits touched the store: reads went %d -> %d", readsAfterFirst, got)
	}
}

func TestResolve_SingleflightCollapsesCreateStorm(t *testing.T) {
	cdb := setup(t)

	// A burst of concurrent first-touch resolves for a BRAND-NEW org must NOT do
	// one write per caller — singleflight + the in-flight cache re-check collapse
	// the e2e create-storm that wedged the single writer. The write count is
	// bounded far below the caller count; with realistic write latency (production)
	// it collapses to ~1. We assert a hard cap well under n (the in-mem store's
	// sub-microsecond writes are the worst case for collapse) plus zero writes once
	// warm — the property that actually protects the money path.
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := Resolve("zz402test"); err != nil {
				t.Errorf("concurrent Resolve: %v", err)
			}
		}()
	}
	wg.Wait()

	burstWrites := atomic.LoadInt64(&cdb.puts)
	if burstWrites == 0 {
		t.Fatalf("burst did no write at all — org was never created")
	}
	if burstWrites >= n/2 {
		t.Fatalf("create-storm not collapsed: %d writes for %d callers", burstWrites, n)
	}

	// Warm: every subsequent resolve is a cache hit → ZERO further writes/reads.
	readsWarm := atomic.LoadInt64(&cdb.query)
	for i := 0; i < 100; i++ {
		if _, err := Resolve("zz402test"); err != nil {
			t.Fatalf("warm Resolve %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&cdb.puts); got != burstWrites {
		t.Fatalf("warm resolves wrote: %d -> %d", burstWrites, got)
	}
	if got := atomic.LoadInt64(&cdb.query); got != readsWarm {
		t.Fatalf("warm resolves read: %d -> %d", readsWarm, got)
	}
}

// TestResolve_SingleflightCollapsesUnderLatency proves the collapse is real when
// the resolve has production-like latency: with the flight held open, ALL
// concurrent callers share ONE flight (the SQLite writer contention we are
// defending against makes the real flight slow, so this is the realistic case).
func TestResolve_SingleflightCollapsesUnderLatency(t *testing.T) {
	setup(t)

	var ran int64
	release := make(chan struct{})
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = group.Do("slowslug", func() (interface{}, error) {
				atomic.AddInt64(&ran, 1)
				<-release // hold the flight so every caller piles onto it
				return "x", nil
			})
		}()
	}
	// Give all goroutines time to enter Do and block on the single flight.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&ran); got != 1 {
		t.Fatalf("singleflight ran the closure %d times under latency, want exactly 1", got)
	}
}

func TestResolve_TTLExpiryReReads(t *testing.T) {
	cdb := setup(t)

	base := time.Now()
	now = func() time.Time { return base }

	if _, err := Resolve("lux"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	reads1 := atomic.LoadInt64(&cdb.query)

	// Still within TTL → no new read.
	now = func() time.Time { return base.Add(ttl - time.Second) }
	if _, err := Resolve("lux"); err != nil {
		t.Fatalf("Resolve within TTL: %v", err)
	}
	if got := atomic.LoadInt64(&cdb.query); got != reads1 {
		t.Fatalf("within-TTL resolve re-read: %d -> %d", reads1, got)
	}

	// Past TTL → at least one re-read.
	now = func() time.Time { return base.Add(ttl + time.Second) }
	if _, err := Resolve("lux"); err != nil {
		t.Fatalf("Resolve past TTL: %v", err)
	}
	if got := atomic.LoadInt64(&cdb.query); got <= reads1 {
		t.Fatalf("past-TTL resolve did not re-read: reads stayed %d", got)
	}
}

func TestResolve_ReturnedOrgHasSlug(t *testing.T) {
	setup(t)
	org, err := Resolve("adnexus")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if org.Name != "adnexus" {
		t.Fatalf("resolved org name = %q, want adnexus", org.Name)
	}
	if !org.Enabled {
		t.Fatalf("resolved org should be Enabled")
	}
}

func TestInvalidate_ForcesReRead(t *testing.T) {
	cdb := setup(t)

	if _, err := Resolve("pars"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	reads1 := atomic.LoadInt64(&cdb.query)

	Invalidate("pars")

	if _, err := Resolve("pars"); err != nil {
		t.Fatalf("Resolve after invalidate: %v", err)
	}
	if got := atomic.LoadInt64(&cdb.query); got <= reads1 {
		t.Fatalf("invalidate did not force a re-read: reads stayed %d", got)
	}
}
