package goja

import (
	"github.com/hanzoai/cloud/internal/environ"
	"container/list"
	"context"
	"database/sql"
	"encoding/base32"
	"fmt"
	"strings"
	"sync"
	"time"

	// cloud.OrgNamespace is the ONE check a tenant string passes through to become
	// the name of a database.
	"github.com/hanzoai/cloud"
	// cek is the ONE opener: each tenant's database is born encrypted under the
	// key cek derives from the process master and that tenant's namespace.
	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"

	// The ONE "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
)

// tenantEnc is the ONE tenant→object-key encoder. Lowercased, unpadded base32
// (RFC 4648 alphabet a-z2-7) of the RAW org bytes. See TenantSegment.
var tenantEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// TenantSegment maps a tenant/org key to an INJECTIVE, traversal-safe single
// OBJECT-STORE key segment: lowercased unpadded base32 of the RAW org bytes.
//
// This is deliberately NOT a "slug": lowercasing + folding illegal bytes to '_'
// (the previous slugify) is NON-injective and collapses DISTINCT owners onto ONE
// physical prefix — a cross-tenant break. principal.Org returns the org VERBATIM
// for exactly that reason (folding "Acme"/"acme" or "a b"/"a_b" into one bucket is
// itself a tenant break); this encoder preserves that distinction all the way to
// the stored name. base32 of the raw bytes is a bijection with its output, so
// distinct orgs ALWAYS map to distinct segments: "Acme", "acme", "a b" and "a_b"
// each land on their OWN prefix (the previous slugify collapsed all four onto two).
//
// The [a-z2-7] output contains no path separator and can never equal "." or ".."
// (those need chars outside the alphabet), so a segment can never traverse an
// object-store key prefix. Empty tenant → "" (callers reject it).
//
// It does NOT name the per-tenant DATABASE. That name is namespace's — one
// slugger for every file cloud opens — and this encoder is what remains for the
// blob half, which has no namespace rendering of its own.
func TenantSegment(tenant string) string {
	if tenant == "" {
		return ""
	}
	return strings.ToLower(tenantEnc.EncodeToString([]byte(tenant)))
}

// Cache sizing (env-overridable). The per-tenant *sql.DB handles are pooled with
// an LRU cap + idle eviction so a binary serving N tenants never holds N open
// SQLite handles at once (each handle is a WAL file + fd + memory) — reopening an
// evicted tenant on next touch is cheap. Only IDLE handles (no in-flight dispatch)
// are ever closed, so eviction can never yank a DB out from under a live request.
const (
	defaultMaxOpen = 256             // max concurrently-open tenant DBs
	defaultIdleTTL = 5 * time.Minute // close a tenant DB idle at least this long
)

// stores is a hand-rolled hanzoai/orm/db.Namespaces — the same design (bound on
// open handles, idle sweep, never evict while pinned, single-flighted opens)
// arrived at independently — and it is not collapsed onto it yet, deliberately:
// orm/db.Namespaces takes its own string-typed db.Namespace rather than a
// namespace.Namespace, so adopting it today would bring a THIRD naming type into
// cloud — adding a way rather than removing one. When orm/db takes the value,
// this pool is the first thing that should go.
//
// stores is the per-tenant SQLite manager: ONE database FILE per tenant
// ({DataDir}/orgs/{slug}/{name}.db, rendered by namespace like every other file
// cloud opens), the "Prod = SQLite per tenant" rule (HIP-0302). Files open lazily
// on first use, migrate once (the subsystem's Schema DDL), run the optional OnOpen
// seed, and are pooled (LRU-capped, idle-evicted). Concurrent opens of the same
// tenant are single-flighted under mu.
type stores struct {
	name    string
	dataDir string
	schema  string
	onOpen  func(ctx context.Context, tenant string, db *sql.DB) error

	maxOpen int
	idleTTL time.Duration

	mu sync.Mutex
	// m is keyed by the NAMESPACE, which is what names the file: two spellings
	// that render one database cannot become two pooled handles on it.
	m   map[namespace.Namespace]*entry
	lru *list.List // *entry, front = most-recently-used, back = LRU
}

// entry is one pooled tenant DB. inUse counts in-flight dispatches holding it; an
// entry is evictable only when inUse==0 (never close a DB mid-transaction).
type entry struct {
	ns       namespace.Namespace
	db       *sql.DB
	inUse    int
	lastUsed time.Time
	el       *list.Element
}

func newStores(name, dataDir, schema string, onOpen func(context.Context, string, *sql.DB) error) *stores {
	return &stores{
		name:    name,
		dataDir: dataDir,
		schema:  schema,
		onOpen:  onOpen,
		maxOpen: environ.Int("CLOUD_GOJABASE_MAX_DBS", defaultMaxOpen),
		idleTTL: time.Duration(environ.Int("CLOUD_GOJABASE_IDLE_TTL_SEC", int(defaultIdleTTL/time.Second))) * time.Second,
		m:       make(map[namespace.Namespace]*entry),
		lru:     list.New(),
	}
}

// acquire returns the tenant's *sql.DB (opening+migrating+seeding it on first use)
// PINNED for one dispatch, plus a release func the caller MUST call when the
// dispatch (and its transaction) finishes. While pinned (inUse>0) the entry is
// never evicted, so the returned handle stays valid for the whole request even if
// the pool is over capacity. Concurrent acquires of the same tenant share the one
// handle (single-flighted under mu).
func (s *stores) acquire(ctx context.Context, tenant string) (*sql.DB, func(), error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, nil, fmt.Errorf("gojabase[%s]: empty tenant", s.name)
	}
	ns, err := cloud.OrgNamespace(tenant, "")
	if err != nil {
		return nil, nil, fmt.Errorf("gojabase[%s]: %w", s.name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepIdleLocked(time.Now())

	if e, ok := s.m[ns]; ok {
		e.inUse++
		e.lastUsed = time.Now()
		s.lru.MoveToFront(e.el)
		return e.db, s.releaser(e), nil
	}

	// Make room: close idle (inUse==0) LRU tails down toward the cap. If every
	// entry is pinned we exceed the cap transiently rather than close a live DB.
	s.evictToCapLocked()

	db, err := s.openLocked(ctx, ns, tenant)
	if err != nil {
		return nil, nil, err
	}
	e := &entry{ns: ns, db: db, inUse: 1, lastUsed: time.Now()}
	e.el = s.lru.PushFront(e)
	s.m[ns] = e
	return e.db, s.releaser(e), nil
}

// releaser decrements the pin count for one entry (called when a dispatch ends).
func (s *stores) releaser(e *entry) func() {
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.inUse > 0 {
			e.inUse--
		}
		e.lastUsed = time.Now()
	}
}

// openLocked opens+pragma+migrates+seeds a tenant DB. Runs under s.mu so a tenant
// is opened exactly once (single-flight). The pragmas/migration/seed use ctx. It
// takes the namespace (which names the file) AND the raw tenant (which the OnOpen
// seed is written in terms of).
func (s *stores) openLocked(ctx context.Context, ns namespace.Namespace, tenant string) (*sql.DB, error) {
	db, err := cek.Open(ns, s.name, s.dataDir)
	if err != nil {
		return nil, fmt.Errorf("gojabase[%s]: open %s: %w", s.name, ns, err)
	}
	// MaxOpenConns(1) serializes writes against the single-writer file; the
	// per-request transaction (see Dispatch) then holds that one connection for
	// the whole dispatch, so a request is atomic.
	sqlpool.Single(db)
	if strings.TrimSpace(s.schema) != "" {
		if _, err := db.ExecContext(ctx, s.schema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("gojabase[%s]: migrate: %w", s.name, err)
		}
	}
	if s.onOpen != nil {
		if err := s.onOpen(ctx, tenant, db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("gojabase[%s]: onOpen(%s): %w", s.name, tenant, err)
		}
	}
	return db, nil
}

// evictToCapLocked closes idle LRU-tail entries until the pool is under maxOpen.
// Stops early if the oldest closable entry is pinned (all remaining are in use) —
// a live DB is never closed. Caller holds s.mu.
func (s *stores) evictToCapLocked() {
	for len(s.m) >= s.maxOpen {
		e := s.oldestClosableLocked()
		if e == nil {
			return // every entry is pinned; exceed the cap transiently
		}
		s.closeEntryLocked(e)
	}
}

// sweepIdleLocked closes every unpinned entry idle at least idleTTL. Caller holds
// s.mu. Cheap: reopening an evicted tenant on next touch just re-runs open+migrate.
func (s *stores) sweepIdleLocked(now time.Time) {
	if s.idleTTL <= 0 {
		return
	}
	for el := s.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*entry)
		if e.inUse == 0 && now.Sub(e.lastUsed) >= s.idleTTL {
			s.closeEntryLocked(e)
		}
		el = prev
	}
}

// oldestClosableLocked returns the least-recently-used UNPINNED entry, or nil if
// every open entry is currently pinned. Caller holds s.mu.
func (s *stores) oldestClosableLocked() *entry {
	for el := s.lru.Back(); el != nil; el = el.Prev() {
		if e := el.Value.(*entry); e.inUse == 0 {
			return e
		}
	}
	return nil
}

// closeEntryLocked closes+removes one entry from the pool. Caller holds s.mu.
func (s *stores) closeEntryLocked(e *entry) {
	_ = e.db.Close()
	s.lru.Remove(e.el)
	delete(s.m, e.ns)
}

// closeAll closes every open tenant DB and clears the pool. Idempotent. Called at
// shutdown, so it closes even pinned entries (no dispatch survives shutdown).
func (s *stores) closeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, e := range s.m {
		if err := e.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.m = make(map[namespace.Namespace]*entry)
	s.lru.Init()
	return firstErr
}
