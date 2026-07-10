// Package infra provides infrastructure clients.
//
// This file implements the KV client used for caching, org-id resolution, and
// the distributed lock. By DEFAULT it is served by hanzo/base — a per-org/user
// embedded SQLite store, so commerce runs WITHOUT any external datastore. When
// KV_URL is set, the SAME client is instead backed by an external Hanzo KV
// instance (github.com/hanzoai/kv-go — brand-correct, NOT raw go-redis), selected
// at construction. The method set the rest of commerce consumes (Get/Set/Delete/
// Exists/Health/Close plus the SetNX-family the distributed lock builds on) is
// identical either way, so nothing downstream — including pkg/org's KVCache —
// knows or cares which backend is live.
//
// The KVCache interface (string-valued Get/Set, variadic Delete) is what
// pkg/org binds to. KVClient satisfies it so org-id caching is unchanged.
package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/store"
	redis "github.com/hanzoai/kv-go/v9"
)

// KVConfig holds the base-backed KV configuration. Base needs only a data
// directory and an optional key prefix; the external-instance backend is
// selected out-of-band by KV_URL (NewKVClientFromURL), not by a field here.
type KVConfig struct {
	// Enabled enables the KV service.
	Enabled bool

	// DataDir is the filesystem path for the base SQLite store. When empty,
	// the manager supplies a default under the app data dir.
	DataDir string

	// DataDSN optionally routes the store at Postgres (multi-instance
	// deployments). Empty selects the file-path SQLite default under DataDir.
	DataDSN string

	// KeyPrefix is prepended to all keys (namespacing within one store).
	KeyPrefix string
}

// kvBackend is the storage engine behind KVClient: Hanzo Base (SQLite, the
// default) or an external Hanzo KV instance (KV_URL). Both satisfy the SAME
// primitive set so KVClient — and the distributed lock built on it — are
// backend-agnostic. A miss returns store.ErrKVNotFound uniformly.
type kvBackend interface {
	get(key string) ([]byte, error)
	set(key string, value []byte, ttl time.Duration) error
	del(key string) error
	exists(key string) (bool, error)
	setNX(key string, value []byte, ttl time.Duration) (bool, error)
	compareAndDelete(key string, want []byte) (bool, error)
	compareAndExtend(key string, want []byte, ttl time.Duration) (bool, error)
	ping(ctx context.Context) error
	close() error
}

// baseBackend is the Hanzo Base (SQLite) engine. st is non-nil only when this
// backend OWNS the store (NewKVClient), so close releases exactly what it opened.
type baseBackend struct {
	kv *store.KVStore
	st *store.Store
}

func (b *baseBackend) get(k string) ([]byte, error)                    { return b.kv.Get(k) }
func (b *baseBackend) set(k string, v []byte, ttl time.Duration) error { return b.kv.Set(k, v, ttl) }
func (b *baseBackend) del(k string) error                              { return b.kv.Delete(k) }
func (b *baseBackend) exists(k string) (bool, error)                   { return b.kv.Exists(k) }
func (b *baseBackend) setNX(k string, v []byte, ttl time.Duration) (bool, error) {
	return b.kv.SetNX(k, v, ttl)
}
func (b *baseBackend) compareAndDelete(k string, w []byte) (bool, error) {
	return b.kv.CompareAndDelete(k, w)
}
func (b *baseBackend) compareAndExtend(k string, w []byte, ttl time.Duration) (bool, error) {
	return b.kv.CompareAndExtend(k, w, ttl)
}
func (b *baseBackend) ping(context.Context) error {
	// Local SQLite store; a cheap existence probe forces a real query.
	_, err := b.kv.Exists("__ping__")
	return err
}
func (b *baseBackend) close() error {
	if b.st != nil {
		return b.st.Close(context.Background())
	}
	return nil
}

// casDelScript / casExtendScript give the external backend the same atomic
// compare-and-delete / compare-and-extend the base store gets from a serializable
// SQLite transaction — one server-side script, no read-then-write race.
//
// EVICTION SAFETY — guard before wiring a money op to the lock's .Acquire()
// (locking.go). These CAS primitives, and the distributed lock built on
// SetNX + CAS, are correct ONLY while a HELD lock key cannot be evicted out from
// under its holder. An eviction policy that drops live keys under memory pressure
// (allkeys-lru / allkeys-random — which is exactly the default a dedicated Hanzo
// KV *cache* instance provisions: see cloud clients/provisioning dedicated.go)
// would let a still-valid lock vanish, so a second Acquire's SetNX succeeds and
// TWO holders enter the "mutually exclusive" section — a double refund / double
// payout. The lock is currently DORMANT (no money caller). Before ANY
// money-moving op takes .Acquire() on an EXTERNAL KV instance, that instance's
// lock keyspace MUST be on a non-evicting policy (maxmemory-policy noeviction, or
// a dedicated lock DB/instance whose keys never expire under pressure) — never
// the shared allkeys-lru cache DB.
var (
	casDelScript    = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)
	casExtendScript = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) else return 0 end`)
)

// redisBackend is the external Hanzo KV engine over the kv-go client.
type redisBackend struct{ c *redis.Client }

func (r *redisBackend) get(k string) ([]byte, error) {
	v, err := r.c.Get(context.Background(), k).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, store.ErrKVNotFound
	}
	return v, err
}
func (r *redisBackend) set(k string, v []byte, ttl time.Duration) error {
	return r.c.Set(context.Background(), k, v, ttl).Err()
}
func (r *redisBackend) del(k string) error { return r.c.Del(context.Background(), k).Err() }
func (r *redisBackend) exists(k string) (bool, error) {
	n, err := r.c.Exists(context.Background(), k).Result()
	return n > 0, err
}
func (r *redisBackend) setNX(k string, v []byte, ttl time.Duration) (bool, error) {
	return r.c.SetNX(context.Background(), k, v, ttl).Result()
}
func (r *redisBackend) compareAndDelete(k string, want []byte) (bool, error) {
	n, err := casDelScript.Run(context.Background(), r.c, []string{k}, want).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
func (r *redisBackend) compareAndExtend(k string, want []byte, ttl time.Duration) (bool, error) {
	n, err := casExtendScript.Run(context.Background(), r.c, []string{k}, want, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
func (r *redisBackend) ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }
func (r *redisBackend) close() error                   { return r.c.Close() }

// KVClient is the backend-agnostic cache + lock adapter.
type KVClient struct {
	config  *KVConfig
	backend kvBackend
	owns    bool // Close releases the backend iff this client constructed it
}

// NewKVClient opens (or creates) a Base store and returns a KV client over its
// commerce_kv collection. The store is owned by this client and released on
// Close.
func NewKVClient(_ context.Context, cfg *KVConfig) (*KVClient, error) {
	s, err := store.New(store.Config{
		DataDir: cfg.DataDir,
		DataDSN: cfg.DataDSN,
	})
	if err != nil {
		return nil, fmt.Errorf("kv: open base store: %w", err)
	}
	return &KVClient{config: cfg, backend: &baseBackend{kv: s.KV, st: s}, owns: true}, nil
}

// NewKVClientFromStore adapts an already-constructed Base store (the common case
// — commerce builds one store for the whole process). The caller retains
// ownership of the store; Close here does NOT release it.
func NewKVClientFromStore(cfg *KVConfig, s *store.Store) *KVClient {
	return &KVClient{config: cfg, backend: &baseBackend{kv: s.KV}}
}

// NewKVClientFromURL backs the client with an external Hanzo KV instance reached
// over kvURL (redis://[:pass@]host:port[/db]) — the KV_URL selection. The method
// set is identical to the Base-backed client, so every caller is unchanged. A
// malformed URL is a FAIL-CLOSED config error, never a silent fallback to Base.
func NewKVClientFromURL(cfg *KVConfig, kvURL string) (*KVClient, error) {
	opts, err := redis.ParseURL(kvURL)
	if err != nil {
		return nil, fmt.Errorf("kv: parse KV_URL: %w", err)
	}
	return &KVClient{config: cfg, backend: &redisBackend{c: redis.NewClient(opts)}, owns: true}, nil
}

// key returns the full key with prefix.
func (c *KVClient) key(k string) string {
	if c.config == nil || c.config.KeyPrefix == "" {
		return k
	}
	return c.config.KeyPrefix + ":" + k
}

// Get retrieves a value by key. A miss returns ("", nil) — matching the
// nil-as-empty contract pkg/org depends on.
func (c *KVClient) Get(ctx context.Context, key string) (string, error) {
	val, err := c.backend.get(c.key(key))
	if errors.Is(err, store.ErrKVNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kv get failed: %w", err)
	}
	return string(val), nil
}

// GetJSON retrieves and unmarshals a JSON value. A miss leaves dst untouched.
func (c *KVClient) GetJSON(ctx context.Context, key string, dst interface{}) error {
	val, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	if val == "" {
		return nil
	}
	return json.Unmarshal([]byte(val), dst)
}

// Set stores a value with optional expiration (ttl==0 means no expiry).
func (c *KVClient) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if err := c.backend.set(c.key(key), []byte(value), ttl); err != nil {
		return fmt.Errorf("kv set failed: %w", err)
	}
	return nil
}

// SetJSON marshals and stores a value.
func (c *KVClient) SetJSON(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kv json marshal failed: %w", err)
	}
	return c.Set(ctx, key, string(data), ttl)
}

// Delete removes one or more keys. Deleting an absent key is not an error.
func (c *KVClient) Delete(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if err := c.backend.del(c.key(k)); err != nil {
			return fmt.Errorf("kv delete failed: %w", err)
		}
	}
	return nil
}

// Exists returns the count of keys that currently hold a live value.
func (c *KVClient) Exists(ctx context.Context, keys ...string) (int64, error) {
	var n int64
	for _, k := range keys {
		ok, err := c.backend.exists(c.key(k))
		if err != nil {
			return 0, fmt.Errorf("kv exists failed: %w", err)
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// SetNX / CompareAndDelete / CompareAndExtend are the atomic primitives the
// distributed lock (locking.go) builds on. They take a LOGICAL key (prefixed
// here, once), so the lock key layout is identical across backends.
func (c *KVClient) SetNX(key string, value []byte, ttl time.Duration) (bool, error) {
	return c.backend.setNX(c.key(key), value, ttl)
}

// CompareAndDelete deletes key iff its current value equals want (atomic).
func (c *KVClient) CompareAndDelete(key string, want []byte) (bool, error) {
	return c.backend.compareAndDelete(c.key(key), want)
}

// CompareAndExtend renews key's TTL iff its current value equals want (atomic).
func (c *KVClient) CompareAndExtend(key string, want []byte, ttl time.Duration) (bool, error) {
	return c.backend.compareAndExtend(c.key(key), want, ttl)
}

// Health checks the backing store with a round-trip set/get/delete on a
// reserved health key.
func (c *KVClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()
	const probe = "__health__"
	if err := c.backend.set(c.key(probe), []byte("1"), time.Second); err != nil {
		return HealthStatus{Healthy: false, Latency: time.Since(start), Error: err.Error()}
	}
	if _, err := c.backend.get(c.key(probe)); err != nil {
		return HealthStatus{Healthy: false, Latency: time.Since(start), Error: err.Error()}
	}
	_ = c.backend.del(c.key(probe))
	return HealthStatus{Healthy: true, Latency: time.Since(start)}
}

// Close releases the backend if this client owns it (NewKVClient /
// NewKVClientFromURL). A client adapted from a shared Base store
// (NewKVClientFromStore) leaves the store untouched.
func (c *KVClient) Close() error {
	if c.owns {
		return c.backend.close()
	}
	return nil
}
