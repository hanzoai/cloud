// Copyright © 2026 Hanzo AI. MIT License.

// Package org provides KV-cached org resolution from gateway-supplied
// X-Org-Id headers. The trust boundary lives in pkg/auth — this package
// is the lookup-side cache, separate so callers can bind a KV without
// pulling in the auth middleware.
package org

import (
	"context"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

// KVCache is the minimal interface required for org-id caching.
// *infra.KVClient satisfies it.
type KVCache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

var (
	mu      sync.RWMutex
	kvCache KVCache
	ttl     = 5 * time.Minute
)

// Bind wires the KV client. nil disables caching.
func Bind(kv KVCache) {
	mu.Lock()
	defer mu.Unlock()
	kvCache = kv
}

func cacheKey(name string) string { return "iam:org_by_name:" + name }

// Resolve loads an organization from a gateway-supplied owner name. It
// reads-through the KV cache and falls back to GetOrCreate, which auto-
// provisions the org record on first encounter.
//
// Caller is responsible for providing the right context — typically
// a request-scoped context.WithTimeout from the handler.
func Resolve(ctx context.Context, name string) (*organization.Organization, error) {
	// Never provision an org from a bearer-shaped identifier. `name` is the
	// gateway-supplied X-Org-Id; a caller who presents a raw API key as their
	// bearer would otherwise cause GetOrCreate below to persist the key as an
	// org name and tenant id, leaking the secret (incident 2026-07-02).
	if organization.IsSecretLikeName(name) {
		return nil, organization.ErrSecretLikeName
	}

	mu.RLock()
	kv := kvCache
	mu.RUnlock()

	db := datastore.New(ctx)
	o := organization.New(db)
	o.Name = name
	o.Enabled = true

	if kv != nil {
		if id, err := kv.Get(ctx, cacheKey(name)); err == nil && id != "" {
			if err := o.GetById(id); err == nil {
				return o, nil
			}
		}
	}

	if err := o.GetOrCreate("Name=", name); err != nil {
		return nil, err
	}
	if kv != nil {
		_ = kv.Set(ctx, cacheKey(name), o.Id(), ttl)
	}
	return o, nil
}
