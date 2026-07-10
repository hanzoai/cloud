// Package svcorg resolves — and memoizes — the organization a verified service
// token acts on behalf of (cloud-api → commerce per-org billing).
//
// WHY THIS EXISTS (money-critical, incident 2026-07-04):
//
// The service-token auth branch used to call org.GetOrCreate("Name=", slug) on
// EVERY request, on the caller's cancelable HTTP request context. Under
// concurrent load (many orgs, e2e first-touch of NEW orgs, the 15-min
// auto-recharge cron all sharing commerce's single SQLite writer), that
// read-then-maybe-create serialized behind the writer; when the wait exceeded
// cloud-api's request deadline the query returned "context canceled", the auth
// branch fell through to the legacy per-org-token path, tried to Peek the 64-hex
// service token as a JWT ("Invalid Segments"), and 401'd — so cloud-api's balance
// gate fail-secured to 402 and real customers could not run paid inference.
//
// This package removes the per-request datastore hit from the steady state and
// decouples resolution from the caller's deadline:
//
//   - CACHE: an LRU keyed by org slug with a short TTL. A hit does ZERO datastore
//     work — the common path (org already exists) touches no SQLite at all.
//   - SINGLEFLIGHT: a cache miss for a slug collapses concurrent resolvers into
//     ONE datastore GetOrCreate, so a burst of first requests for a brand-new org
//     (the e2e create-storm) performs exactly one create, not N contending writes.
//   - DETACHED + BOUNDED: resolution runs on a fresh context.Background() with its
//     OWN timeout, so a slow resolve fast-fails inside commerce instead of being
//     canceled mid-create by an upstream deadline (which is what left the writer
//     wedged and cascaded to the confusing 401).
//
// It memoizes ONLY the (slug → *Organization) mapping. It does not change org
// derivation, permissions, or per-org isolation — the caller still scopes every
// downstream query by the returned org's Name. The money gate is untouched.
package svcorg

import (
	"context"
	"errors"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

const (
	// cacheSize bounds the LRU. Commerce serves a few thousand orgs at most; 4096
	// hot entries covers the working set with a tiny footprint and evicts the cold
	// tail. Overflow is correctness-neutral: an evicted slug just re-resolves.
	cacheSize = 4096

	// ttl bounds staleness of the cached *Organization. Org rows (Name/Live/
	// Enabled) change rarely; a 60s TTL means a mutation is picked up within a
	// minute while steady-state requests stay datastore-free. Short enough that a
	// disabled/renamed org can't be served stale for long, long enough to absorb
	// a full e2e load burst without re-reading.
	ttl = 60 * time.Second

	// resolveTimeout is commerce's OWN deadline for a cache-miss resolve. It is
	// INDEPENDENT of the caller's request context, so a busy writer fast-fails
	// here (returning a retryable error) rather than hanging until the upstream
	// cancels — which previously wedged the create half-done and cascaded to 401.
	// Comfortably above SQLite's busy_timeout wait for a normal contended write,
	// well below a typical upstream request deadline.
	resolveTimeout = 4 * time.Second
)

// ErrResolveFailed wraps any failure to resolve/provision the org for a verified
// service token. Callers MUST treat it as retryable (HTTP 503) — never as a bad
// credential — because the service token itself was already verified; only the
// backing store hiccuped.
var ErrResolveFailed = errors.New("svcorg: could not resolve organization for service token")

type entry struct {
	org     *organization.Organization
	expires time.Time
}

// cache is the process-wide resolved-org LRU. Package-global by design: the
// resolution is identical for every request (the org row is global, not
// per-tenant), so one shared cache serves the whole binary.
var (
	cache *lru.Cache[string, entry]
	group singleflight.Group
)

func init() {
	// lru.New only errors on a non-positive size, which cacheSize is not; the
	// error is structurally impossible, so a failure here is a programming bug
	// worth surfacing loudly at startup rather than silently degrading.
	c, err := lru.New[string, entry](cacheSize)
	if err != nil {
		panic("svcorg: LRU init: " + err.Error())
	}
	cache = c
}

// now is overridable in tests to drive TTL expiry deterministically.
var now = time.Now

// Resolve returns the organization named slug for a VERIFIED service-token
// request, creating it if it does not yet exist. The slug MUST already be
// validated by the caller (non-empty, not bearer-shaped) — this function trusts
// it and never re-derives policy from it.
//
// Steady state (slug cached, unexpired) does NO datastore work. A miss resolves
// under singleflight on a detached, bounded context so concurrent misses for the
// same slug share one GetOrCreate and a slow store fast-fails with
// ErrResolveFailed instead of blocking on the caller's deadline.
func Resolve(slug string) (*organization.Organization, error) {
	if e, ok := cache.Get(slug); ok && now().Before(e.expires) {
		return e.org, nil
	}

	// Miss (or expired): one resolver per slug across all concurrent callers.
	v, err, _ := group.Do(slug, func() (interface{}, error) {
		// Re-check under the flight: a racer that resolved while we queued
		// populated the cache, so don't hit the store again.
		if e, ok := cache.Get(slug); ok && now().Before(e.expires) {
			return e.org, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()

		org := organization.New(datastore.New(ctx))
		org.Name = slug
		org.Enabled = true

		// GetOrCreate: find by name (read) or create from context (write). Bounded
		// by ctx above, so a wedged writer returns here — it does not hang.
		if err := org.GetOrCreate("Name=", slug); err != nil {
			log.Warn("svcorg: resolve/create org %q failed: %v", slug, err)
			return nil, err
		}

		cache.Add(slug, entry{org: org, expires: now().Add(ttl)})
		return org, nil
	})
	if err != nil {
		return nil, errors.Join(ErrResolveFailed, err)
	}
	return v.(*organization.Organization), nil
}

// Invalidate drops a slug from the cache so the next Resolve re-reads it. Call
// after mutating an org's identity fields (Name/Enabled/Live) so a change is not
// masked by the TTL window. Safe to call for an absent slug.
func Invalidate(slug string) { cache.Remove(slug) }
