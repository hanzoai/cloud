// Package cache (memcache.go) provides a caching abstraction layer that can work with
// different backends (in-memory, Redis, etc.) to replace appengine/memcache.
package cache

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

var (
	// Default cache instance (in-memory for now)
	defaultMemCache = NewMemoryCache()
)

// Item represents a cache item
type Item struct {
	Key        string
	Value      []byte
	Object     interface{}
	Expiration time.Duration
}

// MemoryCache is an in-memory cache implementation
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string]*cacheItem
}

type cacheItem struct {
	value      []byte
	expiration time.Time
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache() *MemoryCache {
	c := &MemoryCache{
		items: make(map[string]*cacheItem),
	}
	// Start background cleanup goroutine
	go c.cleanup()
	return c
}

func (c *MemoryCache) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, item := range c.items {
			if !item.expiration.IsZero() && now.After(item.expiration) {
				delete(c.items, key)
			}
		}
		c.mu.Unlock()
	}
}

// Get retrieves an item from the cache
func (c *MemoryCache) Get(ctx context.Context, key string, dst interface{}) error {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return ErrCacheMiss
	}

	if !item.expiration.IsZero() && time.Now().After(item.expiration) {
		c.Delete(ctx, key)
		return ErrCacheMiss
	}

	return json.Unmarshal(item.value, dst)
}

// Set stores an item in the cache
func (c *MemoryCache) Set(ctx context.Context, item *Item) error {
	var value []byte
	var err error

	if item.Object != nil {
		value, err = json.Marshal(item.Object)
		if err != nil {
			return err
		}
	} else {
		value = item.Value
	}

	var expiration time.Time
	if item.Expiration > 0 {
		expiration = time.Now().Add(item.Expiration)
	}

	c.mu.Lock()
	c.items[item.Key] = &cacheItem{
		value:      value,
		expiration: expiration,
	}
	c.mu.Unlock()

	return nil
}

// Delete removes an item from the cache
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
	return nil
}

// IncrementExisting increments an existing counter
func (c *MemoryCache) IncrementExisting(ctx context.Context, key string, delta int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		return 0, ErrCacheMiss
	}

	var count int64
	if err := json.Unmarshal(item.value, &count); err != nil {
		return 0, err
	}

	count += delta
	value, err := json.Marshal(count)
	if err != nil {
		return 0, err
	}

	item.value = value
	return count, nil
}

// Error types
type CacheError string

func (e CacheError) Error() string { return string(e) }

const (
	ErrCacheMiss CacheError = "cache: miss"
)

// JSON provides JSON codec for cache operations (compatible with memcache.JSON)
var JSON = &jsonCodec{}

type jsonCodec struct{}

func (j *jsonCodec) Get(ctx context.Context, key string, dst interface{}) (*Item, error) {
	err := defaultMemCache.Get(ctx, key, dst)
	if err != nil {
		return nil, err
	}
	return &Item{Key: key, Object: dst}, nil
}

func (j *jsonCodec) Set(ctx context.Context, item *Item) error {
	return defaultMemCache.Set(ctx, item)
}

// Gob provides Gob codec for cache operations (compatible with memcache.Gob)
// For simplicity, we use JSON internally
var Gob = &gobCodec{}

type gobCodec struct{}

func (g *gobCodec) Get(ctx context.Context, key string, dst interface{}) (*Item, error) {
	err := defaultMemCache.Get(ctx, key, dst)
	if err != nil {
		return nil, err
	}
	return &Item{Key: key, Object: dst}, nil
}

func (g *gobCodec) Set(ctx context.Context, item *Item) error {
	return defaultMemCache.Set(ctx, item)
}

// Package-level convenience functions

// Get retrieves an item from the default cache (memcache-compatible API)
func Get(ctx context.Context, key string) (*Item, error) {
	item := &Item{Key: key}
	err := defaultMemCache.Get(ctx, key, &item.Value)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// Set stores an item in the default cache (memcache-compatible API)
func Set(ctx context.Context, item *Item) error {
	return defaultMemCache.Set(ctx, item)
}

// MemGet retrieves an item from the default cache
func MemGet(ctx context.Context, key string, dst interface{}) error {
	return defaultMemCache.Get(ctx, key, dst)
}

// MemSet stores an item in the default cache
func MemSet(ctx context.Context, item *Item) error {
	return defaultMemCache.Set(ctx, item)
}

// MemDelete removes an item from the default cache
func MemDelete(ctx context.Context, key string) error {
	return defaultMemCache.Delete(ctx, key)
}

// IncrementExisting increments an existing counter in the default cache
func IncrementExisting(ctx context.Context, key string, delta int64) (int64, error) {
	return defaultMemCache.IncrementExisting(ctx, key, delta)
}
