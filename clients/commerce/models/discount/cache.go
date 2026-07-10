package discount

import (
	"context"
	"fmt"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/scope"
)

// In-memory cache for discount keys (replaces appengine memcache)
var (
	discountCache     = make(map[string][]iface.Key)
	discountCacheLock sync.RWMutex
)

// Computes cache key, using format:
//
//	discount-keys-organization
//	discount-keys-store-storeId
//	..etc
func keyForScope(scopeType scope.Type, id string) string {
	key := "discount-keys-"
	keyFmt := key + "%s-%s"
	scopeName := string(scopeType)

	switch scopeType {
	case scope.Organization:
		key = key + scopeName
	case scope.Store:
		key = fmt.Sprintf(keyFmt, scopeName, id)
	case scope.Collection:
		key = fmt.Sprintf(keyFmt, scopeName, id)
	case scope.Product:
		key = fmt.Sprintf(keyFmt, scopeName, id)
	case scope.Variant:
		key = fmt.Sprintf(keyFmt, scopeName, id)
	}

	return key
}

// Invalidate cache for all keys in matching scope
func (d *Discount) invalidateCache() error {
	key := keyForScope(d.Scope.Type, d.ScopeId())
	discountCacheLock.Lock()
	delete(discountCache, key)
	discountCacheLock.Unlock()
	return nil
}

// Cache discount keys
func cacheDiscounts(ctx context.Context, key string, keys []iface.Key) error {
	discountCacheLock.Lock()
	discountCache[key] = keys
	discountCacheLock.Unlock()
	return nil
}

// Get cached discount keys
func getCachedDiscounts(ctx context.Context, key string) ([]iface.Key, error) {
	discountCacheLock.RLock()
	keys, ok := discountCache[key]
	discountCacheLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("cache miss for key: %s", key)
	}
	return keys, nil
}

func GetScopedDiscounts(ctx context.Context, sc scope.Type, id string, keyc chan []iface.Key, errc chan error) {
	// Id required for all scopes except organization
	if id == "" && sc != scope.Organization {
		// TODO: Prevent this from happening. Usually due to store id missing on order.
		errc <- nil
		keyc <- make([]iface.Key, 0)
		return
	}

	// Check cache for keys
	key := keyForScope(sc, id)

	log.Debug("Trying to get discounts from cache using key '%s'", key)
	keys, err := getCachedDiscounts(ctx, key)

	// Fetch keys from datastore if that fails
	if err != nil {
		var filter string
		switch sc {
		case scope.Store:
			filter = "Scope.StoreId="
		case scope.Collection:
			filter = "Scope.CollectionId="
		case scope.Product:
			filter = "Scope.ProductId="
		case scope.Variant:
			filter = "Scope.VariantId="
		}

		db := datastore.New(ctx)
		q := Query(db).Filter("Scope.Type=", string(sc))

		if filter != "" {
			q = q.Filter(filter, id)
		}

		if sc == scope.Organization {
			log.Debug("Trying to get discounts from datastore Scope.Type=organization")
		} else {
			log.Debug("Trying to get discounts from datastore Scope.Type=%s, %s%s", sc, filter, id)
		}

		keys, err = q.Filter("Enabled=", true).GetKeys()

		// Cache keys for later
		if err == nil {
			log.Debug("Caching discount keys for later using cache key '%s'", key)
			err = cacheDiscounts(ctx, key, keys)
		}
	}

	// Return with keys
	errc <- err
	keyc <- keys
}
