// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package preblelocality is a reference implementation of the distributed
// prompt-locality scheduling policies studied in hanzoai/cloud#35
// ([Bounty #18], Preble, ICLR '25) under HIP-1345 §2.3.
//
// It provides:
//
//   - a salted, token-bounded LRU prefix cache (HIP-1345 §1.1: tenant salt
//     is mandatory in every cache key),
//   - a replica model with KV slot caps, per-tenant warm residency
//     (cold-start cost), and aggregate prefill/decode throughput, and
//   - two deterministic routing policies: least-loaded and cache-aware
//     locality, scoring residency, queueing and cold-start latency.
//
// The package is dependency-free (standard library only) so it can be
// adopted by the Enso gateway without touching go.mod.
package preblelocality

import (
	"container/list"
	"sync"
)

// CacheKey is a salted prefix-cache key. The tenant salt is mandatory:
// keys MUST NOT be shared across tenants (HIP-1345 §1.1).
type CacheKey struct {
	Tenant  string
	Session string
	Prefix  uint64 // stable content hash of the logical prefix tokens
}

type cacheEntry struct {
	key    CacheKey
	tokens int
}

// PrefixCache is a bounded LRU cache over salted prefix keys, accounted in
// tokens. It is safe for concurrent use.
type PrefixCache struct {
	mu       sync.Mutex
	capacity int
	used     int
	entries  map[CacheKey]*list.Element
	order    *list.List // front = most recently used

	// Cumulative counters (monotonic).
	Lookups   uint64
	Hits      uint64
	Evictions uint64
	Refused   uint64 // publishes refused because the key can never fit
}

// NewPrefixCache returns a cache bounded to capacityTokens resident tokens.
// A non-positive capacity disables caching entirely (publishes are refused).
func NewPrefixCache(capacityTokens int) *PrefixCache {
	if capacityTokens < 0 {
		capacityTokens = 0
	}
	return &PrefixCache{
		capacity: capacityTokens,
		entries:  make(map[CacheKey]*list.Element),
		order:    list.New(),
	}
}

// Lookup returns the resident token count for key and promotes it to
// most-recently-used. ok is false on a miss.
func (c *PrefixCache) Lookup(key CacheKey) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Lookups++
	el, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	c.order.MoveToFront(el)
	c.Hits++
	return el.Value.(*cacheEntry).tokens, true
}

// Peek returns the resident token count for key without promoting it.
// Routing policies use Peek so that scoring never reshapes the recency
// order of the cache it scores.
func (c *PrefixCache) Peek(key CacheKey) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Lookups++
	el, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	return el.Value.(*cacheEntry).tokens, true
}

// Publish inserts or promotes key with the given token cost, evicting
// least-recently-used entries while the budget is exceeded.
//
// Fail-closed semantics: a publish with tokens <= 0 is ignored (never
// publish empty blocks), and a publish whose token cost exceeds the whole
// budget is refused outright (it would evict everything on every insert).
func (c *PrefixCache) Publish(key CacheKey, tokens int) {
	if tokens <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if tokens > c.capacity {
		c.Refused++
		return
	}
	if el, ok := c.entries[key]; ok {
		old := el.Value.(*cacheEntry)
		if old.tokens == tokens {
			c.order.MoveToFront(el)
			return
		}
		// Re-insert with the updated cost.
		c.used -= old.tokens
		c.order.Remove(el)
		delete(c.entries, key)
	}
	for c.used+tokens > c.capacity {
		back := c.order.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*cacheEntry)
		c.order.Remove(back)
		delete(c.entries, evicted.key)
		c.used -= evicted.tokens
		c.Evictions++
	}
	el := c.order.PushFront(&cacheEntry{key: key, tokens: tokens})
	c.entries[key] = el
	c.used += tokens
}

// Used reports the currently resident token count.
func (c *PrefixCache) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// Len reports the number of resident keys.
func (c *PrefixCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Capacity reports the configured token budget.
func (c *PrefixCache) Capacity() int { return c.capacity }
