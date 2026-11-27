// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/ai/address"
	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/graph"
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
	dir       string // {DataDir}/base
	discovery string // OIDC discovery URL whose jwks_uri is bound onto every per-org app (empty ⇒ unset)
	log       luxlog.Logger

	maxOpen int
	idleTTL time.Duration

	mu      sync.Mutex
	m       map[string]*appEntry // segment → entry
	opening map[string]*opening  // segment → the open in flight
	lru     *list.List           // *appEntry, front = most-recently-used, back = LRU

	// changes carries every record write of the pooled Bases onto the bus; nil
	// publishes nothing.
	changes *changes
}

// appEntry is one pooled per-org Base app + the handler that answers for it,
// compiled once when the app opens. inUse counts in-flight requests holding it;
// an entry is evictable only when inUse==0 (never close an app mid-request).
type appEntry struct {
	seg      string
	app      *baseapp.Base
	handler  zip.Handler
	graph    *graph.Graph // the Base's records as GraphQL, asked through the same router
	inUse    int
	lastUsed time.Time
	el       *list.Element
}

func newPool(root string, deps cloud.Deps, log luxlog.Logger) *pool {
	return &pool{
		dir:       root,
		discovery: deps.Discovery,
		log:       log,
		maxOpen:   env.Int("CLOUD_BASE_MAX_APPS", defaultMaxApps),
		idleTTL:   time.Duration(env.Int("CLOUD_BASE_IDLE_TTL_SEC", int(defaultIdleTTL/time.Second))) * time.Second,
		m:         make(map[string]*appEntry),
		opening:   make(map[string]*opening),
		lru:       list.New(),
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
		if errors.Is(err, errOpening) {
			c.Fiber().Response().Header.Set("Retry-After", "5")
			return zip.Errorf(http.StatusServiceUnavailable, "this Base is being opened or moved to hanzo-sql; try again")
		}
		p.log.Error("base: open org app failed", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "base unavailable")
	}
	defer release()
	return h(c)
}

// openWait bounds how long a request waits for the open of its org's Base, which
// is a move when the Base is still a file. A variable so tests can shorten it.
var openWait = 60 * time.Second

// errOpening is what a request answers when the org's Base did not finish opening
// in openWait. Only that org's requests see it.
var errOpening = errors.New("base: the org's Base is still opening")

// opening is the open of one org's Base in flight: the requests for that org
// wait on done, and nothing else does.
type opening struct {
	done chan struct{}
	err  error
}

// begin claims the open of seg. It answers false when another open of it is in
// flight (or the app is already pooled).
func (p *pool) begin(seg string) (*opening, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.m[seg]; ok {
		return nil, false
	}
	if _, ok := p.opening[seg]; ok {
		return nil, false
	}
	o := &opening{done: make(chan struct{})}
	p.opening[seg] = o
	return o, true
}

// end finishes an open begun with begin: it pools e when it opened, and wakes the
// requests waiting on it with its outcome.
func (p *pool) end(seg string, o *opening, e *appEntry, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.opening, seg)
	if err == nil && e != nil {
		p.evictToCapLocked()
		e.lastUsed = time.Now()
		e.el = p.lru.PushFront(e)
		p.m[seg] = e
	}
	o.err = err
	close(o.done)
}

// get returns the org's pooled app, opening it if it is not, and pins it when pin
// is set. The open runs WITHOUT the pool's lock held, single-flighted per org: a
// Base that is being moved to hanzo-sql makes its own org's requests wait (up to
// openWait) and no other org's.
func (p *pool) get(org string, pin bool) (*appEntry, error) {
	if strings.TrimSpace(org) == "" {
		return nil, fmt.Errorf("base: empty org")
	}
	seg := goja.TenantSegment(org)
	deadline := time.NewTimer(openWait)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		p.sweepIdleLocked(time.Now())
		if e, ok := p.m[seg]; ok {
			if pin {
				e.inUse++
			}
			e.lastUsed = time.Now()
			p.lru.MoveToFront(e.el)
			p.mu.Unlock()
			return e, nil
		}
		if w, ok := p.opening[seg]; ok {
			p.mu.Unlock()
			select {
			case <-w.done:
				if w.err != nil {
					return nil, w.err
				}
				continue
			case <-deadline.C:
				return nil, errOpening
			}
		}
		o := &opening{done: make(chan struct{})}
		p.opening[seg] = o
		p.mu.Unlock()

		e, err := p.open(org, seg)
		if err == nil && pin {
			e.inUse = 1 // before it is pooled, so no sweep can take it
		}
		p.end(seg, o, e, err)
		if err != nil {
			return nil, err
		}
		return e, nil
	}
}

// acquire returns the org's Base handler (opening+migrating the app on first
// use) PINNED for one request, plus a release func the caller MUST call when the
// request finishes. While pinned (inUse>0) the entry is never evicted, so the
// handler stays valid for the whole request even if the pool is over capacity.
func (p *pool) acquire(org string) (zip.Handler, func(), error) {
	e, err := p.get(org, true)
	if err != nil {
		return nil, nil, err
	}
	return e.handler, p.releaser(e), nil
}

// pin returns the org's Base app (opening it if needed) PINNED until release is
// called, for a caller driving the engine's Go API for the length of one call —
// a plane op — so eviction cannot close the app underneath it.
func (p *pool) pin(org string) (*baseapp.Base, func(), error) {
	e, err := p.get(org, true)
	if err != nil {
		return nil, nil, err
	}
	return e.app, p.releaser(e), nil
}

// pinGraph returns the org's graph (opening its Base if needed) PINNED until
// release is called: a GraphQL request asks fields of the Base after the root
// resolver has returned, so the pin lasts the request, not the call.
func (p *pool) pinGraph(org string) (*graph.Graph, func(), error) {
	e, err := p.get(org, true)
	if err != nil {
		return nil, nil, err
	}
	return e.graph, p.releaser(e), nil
}

// appFor returns the org's Base app (opening it if needed) WITHOUT pinning it —
// for in-process callers that drive the engine's Go API directly (collection
// provisioning, seeding) rather than the HTTP path.
func (p *pool) appFor(org string) (*baseapp.Base, error) {
	e, err := p.get(org, false)
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

// open opens+configures+bootstraps+migrates a per-org Base app and compiles its
// handler. It does not touch the pool: [pool.get] pools the result. Where the
// Base lives, and moving it from its files to hanzo-sql first, is [configure]'s.
func (p *pool) open(org, seg string) (*appEntry, error) {
	dir := filepath.Join(p.dir, seg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("base[%s]: mkdir %q: %w", org, dir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg, err := configure(ctx, p.log, org, dir)
	if err != nil {
		if errors.Is(err, locus.ErrMoving) {
			return nil, errOpening
		}
		return nil, fmt.Errorf("base[%s]: place: %w", org, err)
	}
	app := baseapp.NewWithConfig(cfg)

	// IAM-native auth: validate bearer tokens against Hanzo IAM's JWKS as the
	// EXCLUSIVE auth source (no local-password path). Same IAM the cloud edge
	// validates — ONE auth source, not a second path.
	if jwks := p.jwksURL(); jwks != "" {
		app.Store().Set(apis.StoreKeyJWKSURL, jwks)
		app.Store().Set(apis.StoreKeyExternalAuthOnly, true)
	}

	// This Base is the org's and no other's: a token acting in another org is
	// refused by the Base itself, whatever routed the request here.
	app.Store().Set(apis.StoreKeyTenant, org)

	// Its record writes reach the bus.
	if p.changes != nil {
		p.changes.bind(org, app)
	}

	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("base[%s]: bootstrap: %w", org, err)
	}
	if err := app.RunAllMigrations(); err != nil {
		_ = app.ResetBootstrapState()
		return nil, fmt.Errorf("base[%s]: migrations: %w", org, err)
	}
	if err := trustStamp(app); err != nil {
		_ = app.ResetBootstrapState()
		return nil, fmt.Errorf("base[%s]: client address: %w", org, err)
	}
	h, mux, err := handler(app)
	if err != nil {
		_ = app.ResetBootstrapState()
		return nil, fmt.Errorf("base[%s]: build handler: %w", org, err)
	}

	return &appEntry{seg: seg, app: app, handler: h, graph: graph.New(app, mux)}, nil
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

// trustStamp names the header cloud writes the caller's address under
// (geo.Stamp, before any app is reached) as the one a Base reads
// a client's address from. The socket peer of every request a Base sees here
// is the ingress, so without it each limit a Base keys on the address — a
// visitor's writes above all — holds every caller on earth to one count.
func trustStamp(app *baseapp.Base) error {
	s := app.Settings()
	want := []string{address.Header}
	if slices.Equal(s.TrustedProxy.Headers, want) && !s.TrustedProxy.UseLeftmostIP {
		return nil
	}
	s.TrustedProxy.Headers, s.TrustedProxy.UseLeftmostIP = want, false
	return app.Save(s)
}

// jwksURL is the JWKS the provider's discovery document names, or empty when there is
// none yet, which leaves the app's verifier unset exactly as no configured issuer did.
func (p *pool) jwksURL() string {
	if strings.TrimSpace(p.discovery) == "" {
		return ""
	}
	d, err := cloud.DiscoveryFor(p.discovery, nil)
	if err != nil {
		return ""
	}
	doc, err := d.Document()
	if err != nil {
		return ""
	}
	return doc.JWKSURI
}
