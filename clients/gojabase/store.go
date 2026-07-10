package gojabase

import (
	"container/list"
	"context"
	"database/sql"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	// cek opens each per-tenant file encrypted at rest.
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// tenantEnc is the ONE tenant→path-segment encoder. Lowercased, unpadded base32
// (RFC 4648 alphabet a-z2-7) of the RAW org bytes. See TenantSegment.
var tenantEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// TenantSegment maps a tenant/org key to an INJECTIVE, traversal-safe single path
// segment: lowercased unpadded base32 of the RAW org bytes.
//
// This is deliberately NOT a "slug": lowercasing + folding illegal bytes to '_'
// (the previous slugify) is NON-injective and collapses DISTINCT owners onto ONE
// physical store — a cross-tenant break. principal.Org returns the org VERBATIM
// for exactly that reason (folding "Acme"/"acme" or "a b"/"a_b" into one bucket is
// itself a tenant break); this encoder preserves that distinction all the way to
// the on-disk name. base32 of the raw bytes is a bijection with its output, so
// distinct orgs ALWAYS map to distinct segments: "Acme", "acme", "a b" and "a_b"
// each land on their OWN file (the previous slugify collapsed all four onto two).
//
// The [a-z2-7] output contains no path separator and can never equal "." or ".."
// (those need chars outside the alphabet), so a segment can never traverse the
// data tree or an object-store key prefix. Empty tenant → "" (callers reject it).
//
// It is the SINGLE encoding every gojabase tenant-keyed path uses — the per-tenant
// SQLite filename here AND the dataroom object-store key prefix — so a tenant maps
// to exactly ONE physical identity everywhere.
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

// stores is the per-tenant SQLite manager: ONE database FILE per tenant
// ({DataDir}/{name}/{TenantSegment}.db), the "Prod = SQLite per tenant" rule
// (HIP-0302). Files open lazily on first use, migrate once (the subsystem's Schema
// DDL), run the optional OnOpen seed, and are pooled (LRU-capped, idle-evicted).
// Concurrent opens of the same tenant are single-flighted under mu.
type stores struct {
	name   string
	dir    string
	schema string
	onOpen func(ctx context.Context, tenant string, db *sql.DB) error

	maxOpen int
	idleTTL time.Duration

	mu  sync.Mutex
	m   map[string]*entry // segment → entry
	lru *list.List        // *entry, front = most-recently-used, back = LRU
}

// entry is one pooled tenant DB. inUse counts in-flight dispatches holding it; an
// entry is evictable only when inUse==0 (never close a DB mid-transaction).
type entry struct {
	seg      string
	db       *sql.DB
	inUse    int
	lastUsed time.Time
	el       *list.Element
}

func newStores(name, dataDir, schema string, onOpen func(context.Context, string, *sql.DB) error) *stores {
	return &stores{
		name:    name,
		dir:     filepath.Join(dataDir, name),
		schema:  schema,
		onOpen:  onOpen,
		maxOpen: envInt("CLOUD_GOJABASE_MAX_DBS", defaultMaxOpen),
		idleTTL: time.Duration(envInt("CLOUD_GOJABASE_IDLE_TTL_SEC", int(defaultIdleTTL/time.Second))) * time.Second,
		m:       make(map[string]*entry),
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
	seg := TenantSegment(tenant)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepIdleLocked(time.Now())

	if e, ok := s.m[seg]; ok {
		e.inUse++
		e.lastUsed = time.Now()
		s.lru.MoveToFront(e.el)
		return e.db, s.releaser(e), nil
	}

	// Make room: close idle (inUse==0) LRU tails down toward the cap. If every
	// entry is pinned we exceed the cap transiently rather than close a live DB.
	s.evictToCapLocked()

	db, err := s.openLocked(ctx, tenant, seg)
	if err != nil {
		return nil, nil, err
	}
	e := &entry{seg: seg, db: db, inUse: 1, lastUsed: time.Now()}
	e.el = s.lru.PushFront(e)
	s.m[seg] = e
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
// is opened exactly once (single-flight). The pragmas/migration/seed use ctx.
func (s *stores) openLocked(ctx context.Context, tenant, seg string) (*sql.DB, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, fmt.Errorf("gojabase[%s]: mkdir %q: %w", s.name, s.dir, err)
	}
	path := filepath.Join(s.dir, seg+".db")
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gojabase[%s]: open %q: %w", s.name, path, err)
	}
	// MaxOpenConns(1) serializes writes against the single-writer file; the
	// per-request transaction (see Dispatch) then holds that one connection for
	// the whole dispatch, so a request is atomic.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("gojabase[%s]: pragma %q: %w", s.name, pragma, err)
		}
	}
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
	delete(s.m, e.seg)
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
	s.m = make(map[string]*entry)
	s.lru.Init()
	return firstErr
}

// envInt reads a positive int env override, falling back to dflt when unset or
// unparseable (a malformed override can never silently zero a pool bound).
func envInt(key string, dflt int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return dflt
	}
	return n
}
