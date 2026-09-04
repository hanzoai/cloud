// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"container/list"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/goja"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Pool sizing (env-overridable). A full Base app is heavier than a bare *sql.DB
// (its own SQLite handles + hooks), so the pool holds at most maxOpen apps at
// once and evicts the idle LRU tail; reopening an evicted org on next touch is a
// fresh NewWithConfig+Bootstrap+migrate, cheap relative to serving. Only IDLE
// (no in-flight request) apps are ever closed, so eviction can never yank an app
// out from under a live request.
const (
	defaultMaxApps = 64
	defaultIdleTTL = 10 * time.Minute
)

// pool is the per-org Base app manager: ONE Base app per org
// ({DataDir}/base/{TenantSegment}/), the "prod = SQLite per tenant" rule
// (HIP-0302). Apps open lazily on first request, migrate once, and are pooled
// (LRU-capped, idle-evicted). Concurrent opens of the same org are
// single-flighted under mu. The org→segment encoding is goja.TenantSegment —
// the ONE injective, traversal-safe tenant→path encoder, shared so an org maps
// to exactly one physical identity everywhere in the binary.
type pool struct {
	dir     string // {DataDir}/base
	jwksURL string // IAM JWKS endpoint bound onto every per-org app (empty ⇒ unset)
	log     luxlog.Logger

	maxOpen int
	idleTTL time.Duration

	mu  sync.Mutex
	m   map[string]*appEntry // segment → entry
	lru *list.List           // *appEntry, front = most-recently-used, back = LRU
}

// appEntry is one pooled per-org Base app + the handler that answers for it,
// compiled once when the app opens. inUse counts in-flight requests holding it;
// an entry is evictable only when inUse==0 (never close an app mid-request).
type appEntry struct {
	seg      string
	app      *baseapp.Base
	handler  zip.Handler
	inUse    int
	lastUsed time.Time
	el       *list.Element
}

func newPool(root string, deps cloud.Deps, log luxlog.Logger) *pool {
	// cloud.JWKSURLFor, never the suffix concatenated here: this rebuilt the URL
	// inline and so ignored CLOUD_JWKS_URL, leaving an operator-pinned JWKS in
	// force at the edge and not in the per-app pool that verifies the same tokens.
	jwks := ""
	if strings.TrimSpace(deps.IAMIssuer) != "" {
		jwks = cloud.JWKSURLFor(deps.IAMIssuer)
	}
	return &pool{
		dir:     root,
		jwksURL: jwks,
		log:     log,
		maxOpen: environ.Int("CLOUD_BASE_MAX_APPS", defaultMaxApps),
		idleTTL: time.Duration(environ.Int("CLOUD_BASE_IDLE_TTL_SEC", int(defaultIdleTTL/time.Second))) * time.Second,
		m:       make(map[string]*appEntry),
		lru:     list.New(),
	}
}

// serve answers one request from an org's Base.
//
// It is the ONE way a Base answers a request, and both lanes reach it: the
// authenticated /v1/base/* lane, where the org comes from the validated
// principal, and the published-site lane, where it comes from the host's
// resolved Site. The org is the only thing that differs between them, so it is
// the only thing they pass.
func (p *pool) serve(org string, c *zip.Ctx) error {
	h, release, err := p.acquire(org)
	if err != nil {
		p.log.Error("base: open org app failed", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "base unavailable")
	}
	defer release()
	return h(c)
}

// acquire returns the org's Base handler (opening+migrating the app on first
// use) PINNED for one request, plus a release func the caller MUST call when the
// request finishes. While pinned (inUse>0) the entry is never evicted, so the
// handler stays valid for the whole request even if the pool is over capacity.
func (p *pool) acquire(org string) (zip.Handler, func(), error) {
	if strings.TrimSpace(org) == "" {
		return nil, nil, fmt.Errorf("base: empty org")
	}
	seg := goja.TenantSegment(org)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.sweepIdleLocked(time.Now())

	if e, ok := p.m[seg]; ok {
		e.inUse++
		e.lastUsed = time.Now()
		p.lru.MoveToFront(e.el)
		return e.handler, p.releaser(e), nil
	}

	e, err := p.openLocked(org, seg)
	if err != nil {
		return nil, nil, err
	}
	e.inUse = 1
	return e.handler, p.releaser(e), nil
}

// appFor returns the org's Base app (opening it if needed) WITHOUT pinning it —
// for in-process callers that drive the engine's Go API directly (collection
// provisioning, seeding) rather than the HTTP path.
func (p *pool) appFor(org string) (*baseapp.Base, error) {
	seg := goja.TenantSegment(org)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.m[seg]; ok {
		e.lastUsed = time.Now()
		p.lru.MoveToFront(e.el)
		return e.app, nil
	}
	e, err := p.openLocked(org, seg)
	if err != nil {
		return nil, err
	}
	return e.app, nil
}

// releaser decrements the pin count for one entry (called when a request ends).
func (p *pool) releaser(e *appEntry) func() {
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e.inUse > 0 {
			e.inUse--
		}
		e.lastUsed = time.Now()
	}
}

// openLocked makes room, then opens+configures+bootstraps+migrates a per-org
// Base app and compiles its handler, inserting the entry into the pool. Runs
// under p.mu so an org is opened exactly once (single-flight). Caller holds p.mu.
func (p *pool) openLocked(org, seg string) (*appEntry, error) {
	p.evictToCapLocked()

	dir := filepath.Join(p.dir, seg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("base[%s]: mkdir %q: %w", org, dir, err)
	}

	app := baseapp.NewWithConfig(appConfig(dir, org))

	// IAM-native auth: validate bearer tokens against Hanzo IAM's JWKS as the
	// EXCLUSIVE auth source (no local-password path). Same IAM the cloud edge
	// validates — ONE auth source, not a second path.
	if p.jwksURL != "" {
		app.Store().Set(apis.StoreKeyJWKSURL, p.jwksURL)
		app.Store().Set(apis.StoreKeyExternalAuthOnly, true)
	}

	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("base[%s]: bootstrap: %w", org, err)
	}
	if err := app.RunAllMigrations(); err != nil {
		_ = app.ResetBootstrapState()
		return nil, fmt.Errorf("base[%s]: migrations: %w", org, err)
	}
	h, err := handler(app)
	if err != nil {
		_ = app.ResetBootstrapState()
		return nil, fmt.Errorf("base[%s]: build handler: %w", org, err)
	}

	e := &appEntry{seg: seg, app: app, handler: h, lastUsed: time.Now()}
	e.el = p.lru.PushFront(e)
	p.m[seg] = e
	return e, nil
}

// evictToCapLocked closes idle LRU-tail apps until the pool is under maxOpen.
// Stops early if the oldest closable entry is pinned (all remaining are in use) —
// a live app is never closed. Caller holds p.mu.
func (p *pool) evictToCapLocked() {
	for len(p.m) >= p.maxOpen {
		e := p.oldestClosableLocked()
		if e == nil {
			return // every entry is pinned; exceed the cap transiently
		}
		p.closeEntryLocked(e)
	}
}

// sweepIdleLocked closes every unpinned entry idle at least idleTTL. Caller holds
// p.mu. Cheap: reopening an evicted org on next touch just re-runs open+migrate.
func (p *pool) sweepIdleLocked(now time.Time) {
	if p.idleTTL <= 0 {
		return
	}
	for el := p.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*appEntry)
		if e.inUse == 0 && now.Sub(e.lastUsed) >= p.idleTTL {
			p.closeEntryLocked(e)
		}
		el = prev
	}
}

// oldestClosableLocked returns the least-recently-used UNPINNED entry, or nil if
// every open entry is currently pinned. Caller holds p.mu.
func (p *pool) oldestClosableLocked() *appEntry {
	for el := p.lru.Back(); el != nil; el = el.Prev() {
		if e := el.Value.(*appEntry); e.inUse == 0 {
			return e
		}
	}
	return nil
}

// closeEntryLocked closes an app's stores + removes it from the pool. Caller
// holds p.mu. ResetBootstrapState closes the app's DB connections.
func (p *pool) closeEntryLocked(e *appEntry) {
	_ = e.app.ResetBootstrapState()
	p.lru.Remove(e.el)
	delete(p.m, e.seg)
}

// closeAll closes every open per-org app and clears the pool. Idempotent. Called
// at shutdown, so it closes even pinned entries (no request survives shutdown).
func (p *pool) closeAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	for _, e := range p.m {
		if err := e.app.ResetBootstrapState(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	p.m = make(map[string]*appEntry)
	p.lru.Init()
	return firstErr
}
