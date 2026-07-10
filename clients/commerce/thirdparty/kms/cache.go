package kms

import (
	"fmt"
	"sync"
	"time"
)

// cachedEntry holds a secret value with TTL metadata.
type cachedEntry struct {
	value     string
	expiresAt time.Time
}

// CachedClient wraps a KMS Client with a TTL cache.
type CachedClient struct {
	client     *Client
	cache      sync.Map // key -> *cachedEntry
	defaultTTL time.Duration
	failTTL    time.Duration // extended TTL used when KMS is unreachable
}

// NewCachedClient creates a CachedClient wrapping the given KMS Client.
func NewCachedClient(client *Client) *CachedClient {
	return &CachedClient{
		client:     client,
		defaultTTL: 5 * time.Minute,
		failTTL:    30 * time.Minute,
	}
}

// cacheKey builds a map key from path and name.
func cacheKey(secretPath, secretName string) string {
	return secretPath + "/" + secretName
}

// GetSecret retrieves a secret, using the cache when possible.
// On KMS failure, stale cache entries are served with an extended TTL.
func (cc *CachedClient) GetSecret(secretPath, secretName string) (string, error) {
	key := cacheKey(secretPath, secretName)

	// Check cache
	if v, ok := cc.cache.Load(key); ok {
		entry := v.(*cachedEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.value, nil
		}
	}

	// Fetch from KMS
	val, err := cc.client.GetSecretRaw(secretPath, secretName)
	if err != nil {
		// On failure, try to extend stale cache entry
		if v, ok := cc.cache.Load(key); ok {
			entry := v.(*cachedEntry)
			entry.expiresAt = time.Now().Add(cc.failTTL)
			return entry.value, nil
		}
		return "", fmt.Errorf("kms fetch failed and no cached value: %w", err)
	}

	// Store in cache
	cc.cache.Store(key, &cachedEntry{
		value:     val,
		expiresAt: time.Now().Add(cc.defaultTTL),
	})

	return val, nil
}

// Client returns the underlying KMS client (for write operations like SetSecret).
func (cc *CachedClient) Client() *Client {
	return cc.client
}
