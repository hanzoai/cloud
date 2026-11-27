// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	kv "github.com/hanzokv/go/v9"
	"golang.org/x/sync/singleflight"
)

const (
	// DefaultCacheTTL is the short fresh TTL in Hanzo KV for overview and query results.
	DefaultCacheTTL = 15 * time.Second
	// DefaultStaleTTL retains the last-known KV snapshot so Datastore outages serve
	// stale data honestly rather than triggering an unbounded fleet-wide RPC sweep.
	DefaultStaleTTL = 24 * time.Hour
	// DefaultMaxRefreshes bounds concurrent cache-fill refreshes per process.
	DefaultMaxRefreshes = 16
	// cacheKeyVersion namespaces all analytics KV keys for safe schema evolution.
	cacheKeyVersion = "analytics:v1:"
	// cacheStalePrefix stores the last-known snapshot alongside the short-lived fresh key.
	cacheStalePrefix = "analytics:v1:stale:"
)

// KVStore abstracts Hanzo KV operations so tests can inject in-memory or fault-injecting stores.
type KVStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Ping(ctx context.Context) error
}

// KVStatus summarizes Hanzo KV reachability for /v1/analytics/health.
type KVStatus struct {
	// Configured is true when a KV backend URL or store is wired.
	Configured bool `json:"configured"`
	// Reachable is true when the KV store responded to a health ping.
	Reachable bool `json:"reachable"`
	// Error records the last ping error when Configured is true and Reachable is false.
	Error string `json:"error,omitempty"`
}

// HanzoKV implements KVStore over github.com/hanzokv/go/v9.
type HanzoKV struct {
	client *kv.Client
}

// NewHanzoKV connects to Hanzo KV at rawURL; an empty or invalid URL returns nil
// (cold-cache mode: all reads fall straight through to Datastore).
func NewHanzoKV(rawURL string) *HanzoKV {
	if rawURL == "" {
		return nil
	}
	opt, err := kv.ParseURL(rawURL)
	if err != nil {
		return nil
	}
	opt.DialTimeout = 1500 * time.Millisecond
	opt.ReadTimeout = 1500 * time.Millisecond
	opt.WriteTimeout = 1500 * time.Millisecond
	return &HanzoKV{client: kv.NewClient(opt)}
}

// Get fetches raw bytes from Hanzo KV.
func (h *HanzoKV) Get(ctx context.Context, key string) ([]byte, error) {
	if h == nil || h.client == nil {
		return nil, errors.New("kv not configured")
	}
	return h.client.Get(ctx, key).Bytes()
}

// Set stores raw bytes in Hanzo KV with ttl.
func (h *HanzoKV) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if h == nil || h.client == nil {
		return errors.New("kv not configured")
	}
	return h.client.Set(ctx, key, val, ttl).Err()
}

// Ping verifies Hanzo KV reachability.
func (h *HanzoKV) Ping(ctx context.Context) error {
	if h == nil || h.client == nil {
		return errors.New("kv not configured")
	}
	return h.client.Ping(ctx).Err()
}

// Cache is the Hanzo KV acceleration layer with versioned keys, in-process
// singleflight request coalescing, bounded refresh concurrency, and automatic
// stale-on-outage degradation.
type Cache struct {
	kv         KVStore
	ttl        time.Duration
	staleTTL   time.Duration
	sf         singleflight.Group
	refreshSem chan struct{}
	now        func() time.Time
}

// NewCache builds a Cache over store (nil is valid and operates in direct-to-Datastore mode).
func NewCache(store KVStore, ttl, staleTTL time.Duration, maxRefreshes int) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if staleTTL <= 0 {
		staleTTL = DefaultStaleTTL
	}
	if maxRefreshes <= 0 {
		maxRefreshes = DefaultMaxRefreshes
	}
	return &Cache{
		kv:         store,
		ttl:        ttl,
		staleTTL:   staleTTL,
		refreshSem: make(chan struct{}, maxRefreshes),
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// CacheKey constructs the canonical versioned KV key: analytics:v1:<scope>:<hash>.
func CacheKey(scopeKey, payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return cacheKeyVersion + scopeKey + ":" + hex.EncodeToString(sum[:12])
}

func staleKeyFor(freshKey string) string {
	return cacheStalePrefix + freshKey[len(cacheKeyVersion):]
}

// cacheEnvelope wraps a JSON payload with its original assembly timestamp.
type cacheEnvelope struct {
	AsOf    time.Time       `json:"asOf"`
	Payload json.RawMessage `json:"payload"`
}

// CacheMeta describes how a GetOrFetch call was satisfied.
type CacheMeta struct {
	AsOf     time.Time
	CacheHit bool
	Stale    bool
}

// GetOrFetch resolves key from KV if fresh; otherwise coalesces concurrent callers via
// singleflight under a bounded refresh semaphore and fetches from Datastore.
//
// Invariants:
//   - If KV is empty or down, serves straight from Datastore (never fails because of KV).
//   - If Datastore is unavailable, serves the last known KV snapshot with Stale=true.
//   - Never falls back to an unbounded fleet-wide RPC sweep.
func GetOrFetch[T any](ctx context.Context, c *Cache, key string, fetch func(context.Context) (T, error)) (T, CacheMeta, error) {
	var zero T
	if c == nil {
		v, err := fetch(ctx)
		if err != nil {
			return zero, CacheMeta{}, err
		}
		return v, CacheMeta{AsOf: time.Now().UTC()}, nil
	}

	// 1. Fast path: fresh KV hit.
	if v, asOf, ok := readCache[T](ctx, c.kv, key); ok {
		return v, CacheMeta{AsOf: asOf, CacheHit: true, Stale: false}, nil
	}

	// 2. Coalesce concurrent misses for the same key via singleflight.
	type sfResult struct {
		val  T
		meta CacheMeta
	}
	raw, err, _ := c.sf.Do(key, func() (any, error) {
		// Re-check fresh KV in case another goroutine just populated it.
		if v, asOf, ok := readCache[T](ctx, c.kv, key); ok {
			return sfResult{val: v, meta: CacheMeta{AsOf: asOf, CacheHit: true, Stale: false}}, nil
		}

		// Bound concurrent refresh fills.
		select {
		case c.refreshSem <- struct{}{}:
			defer func() { <-c.refreshSem }()
		case <-ctx.Done():
			// Even on context saturation, try stale KV snapshot before failing.
			if v, asOf, ok := readCache[T](context.Background(), c.kv, staleKeyFor(key)); ok {
				return sfResult{val: v, meta: CacheMeta{AsOf: asOf, CacheHit: true, Stale: true}}, nil
			}
			return nil, ctx.Err()
		}

		val, fetchErr := fetch(ctx)
		if fetchErr == nil {
			asOf := c.now()
			writeCache(ctx, c.kv, key, val, asOf, c.ttl, c.staleTTL)
			return sfResult{val: val, meta: CacheMeta{AsOf: asOf, CacheHit: false, Stale: false}}, nil
		}

		// Datastore failed/unavailable: serve last known KV snapshot with stale=true.
		if v, asOf, ok := readCache[T](ctx, c.kv, staleKeyFor(key)); ok {
			return sfResult{val: v, meta: CacheMeta{AsOf: asOf, CacheHit: true, Stale: true}}, nil
		}
		return nil, fetchErr
	})
	if err != nil {
		return zero, CacheMeta{}, err
	}
	res := raw.(sfResult)
	return res.val, res.meta, nil
}

// HasStaleSnapshot reports whether a last-known KV snapshot exists for key.
func (c *Cache) HasStaleSnapshot(ctx context.Context, key string) bool {
	if c == nil || c.kv == nil {
		return false
	}
	if b, err := c.kv.Get(ctx, key); err == nil && len(b) > 0 {
		return true
	}
	if b, err := c.kv.Get(ctx, staleKeyFor(key)); err == nil && len(b) > 0 {
		return true
	}
	return false
}

// Status probes Hanzo KV reachability for /v1/analytics/health.
func (c *Cache) Status(ctx context.Context) KVStatus {
	if c == nil || c.kv == nil {
		return KVStatus{Configured: false, Reachable: false}
	}
	pctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if err := c.kv.Ping(pctx); err != nil {
		return KVStatus{Configured: true, Reachable: false, Error: err.Error()}
	}
	return KVStatus{Configured: true, Reachable: true}
}

func readCache[T any](ctx context.Context, store KVStore, key string) (T, time.Time, bool) {
	var zero T
	if store == nil {
		return zero, time.Time{}, false
	}
	raw, err := store.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return zero, time.Time{}, false
	}
	var env cacheEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Payload) == 0 {
		return zero, time.Time{}, false
	}
	var val T
	if err := json.Unmarshal(env.Payload, &val); err != nil {
		return zero, time.Time{}, false
	}
	return val, env.AsOf.UTC(), true
}

func writeCache[T any](ctx context.Context, store KVStore, key string, val T, asOf time.Time, ttl, staleTTL time.Duration) {
	if store == nil {
		return
	}
	payload, err := json.Marshal(val)
	if err != nil {
		return
	}
	envBytes, err := json.Marshal(cacheEnvelope{
		AsOf:    asOf.UTC(),
		Payload: payload,
	})
	if err != nil {
		return
	}
	// Best-effort writes: KV failure must never break a fresh Datastore answer.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1500*time.Millisecond)
	defer cancel()
	_ = store.Set(wctx, key, envBytes, ttl)
	_ = store.Set(wctx, staleKeyFor(key), envBytes, staleTTL)
}
