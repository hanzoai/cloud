package objects

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"hash/maphash"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"golang.org/x/sync/singleflight"
)

// What a pod remembers between requests, and why none of it needs invalidating.
//
// A pack is immutable and its id is never reused, so a block of one is the same bytes
// for as long as the pack exists: the block cache is keyed by (store, pack, block) and
// is never wrong, only evicted. The index is the one thing that moves: compaction
// swaps an object's row from the packs it replaced to the pack it wrote, and a delete
// removes the repository. A remembered location therefore carries a lifetime (locTTL),
// far shorter than Grace, which keeps every pack a stale location can name on the
// bucket for as long as the location is trusted; and Remove forgets its repository's
// locations on the pod that ran it.
//
// Both caches are bounded in bytes for the whole process, not per Store or per
// request: a pod serves every org it owns from one budget.
const (
	// locTTL is how long a pod trusts an index row it read. It is much shorter than Grace.
	locTTL = 30 * time.Second
	// locCost is what one remembered location is charged: the row, the key and the
	// bookkeeping around them.
	locCost = 200
)

// caches is a pod's memory of pack blocks and index rows.
type caches struct {
	blocks *lru[[]byte]
	locs   *lru[loc]
	flight singleflight.Group
}

func newCaches(blockBytes, locBytes int) *caches {
	return &caches{blocks: newLRU[[]byte](blockBytes), locs: newLRU[loc](locBytes)}
}

var (
	podOnce   sync.Once
	podCaches *caches
)

// pod is the process's caches, sized by GIT_BLOCK_CACHE_BYTES (default 256 MiB) and
// GIT_LOC_CACHE_BYTES (default 64 MiB).
func pod() *caches {
	podOnce.Do(func() {
		podCaches = newCaches(env.Int("GIT_BLOCK_CACHE_BYTES", 256<<20), env.Int("GIT_LOC_CACHE_BYTES", 64<<20))
	})
	return podCaches
}

// token names a Store to the caches that outlive it, so two stores opened over one
// process (a test opens many) never read each other's entries.
func token() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const shards = 16

var seed = maphash.MakeSeed()

// lru is a byte-bounded cache split in shards so concurrent readers of different keys
// do not queue on one lock.
type lru[V any] struct {
	sh [shards]*shard[V]
}

type shard[V any] struct {
	mu        sync.Mutex
	max, used int
	ll        *list.List
	m         map[string]*list.Element
}

type entry[V any] struct {
	k    string
	v    V
	cost int
	exp  int64 // unix nanoseconds, 0 for never
}

func newLRU[V any](max int) *lru[V] {
	c := &lru[V]{}
	for i := range c.sh {
		c.sh[i] = &shard[V]{max: max / shards, ll: list.New(), m: map[string]*list.Element{}}
	}
	return c
}

func (c *lru[V]) of(k string) *shard[V] { return c.sh[maphash.String(seed, k)%shards] }

func (c *lru[V]) get(k string) (V, bool) {
	s := c.of(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[k]
	if !ok {
		var z V
		return z, false
	}
	it := e.Value.(*entry[V])
	if it.exp != 0 && time.Now().UnixNano() > it.exp {
		s.drop(e)
		var z V
		return z, false
	}
	s.ll.MoveToFront(e)
	return it.v, true
}

// put remembers v for ttl (0 for as long as there is room). A value larger than a shard
// is not kept.
func (c *lru[V]) put(k string, v V, cost int, ttl time.Duration) {
	s := c.of(k)
	if cost > s.max {
		return
	}
	var exp int64
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[k]; ok {
		s.drop(e)
	}
	s.m[k] = s.ll.PushFront(&entry[V]{k: k, v: v, cost: cost, exp: exp})
	s.used += cost
	for s.used > s.max {
		s.drop(s.ll.Back())
	}
}

func (s *shard[V]) drop(e *list.Element) {
	it := e.Value.(*entry[V])
	s.used -= it.cost
	delete(s.m, it.k)
	s.ll.Remove(e)
}

// purge forgets every key that starts with prefix.
func (c *lru[V]) purge(prefix string) {
	for _, s := range c.sh {
		s.mu.Lock()
		for k, e := range s.m {
			if strings.HasPrefix(k, prefix) {
				s.drop(e)
			}
		}
		s.mu.Unlock()
	}
}

// bytes is what the cache holds.
func (c *lru[V]) bytes() int {
	n := 0
	for _, s := range c.sh {
		s.mu.Lock()
		n += s.used
		s.mu.Unlock()
	}
	return n
}
