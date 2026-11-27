package goja

import (
	"container/list"
	"context"
	"database/sql"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"

	// orgns.OrgNamespace is the ONE check a tenant string passes through to become
	// the name of a database, and cloud.OrgDB the ONE way it is opened: a SQLite
	// file born encrypted under the key cek derives from the process master and the
	// tenant's namespace, or, with CLOUD_SQL_DSN set, a schema on hanzo-sql.
	"github.com/hanzoai/cloud/sdk"
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
// stores is the per-tenant store manager: ONE database per tenant, opened through
// cloud.OrgDB. That is a FILE ({DataDir}/orgs/{slug}/{name}.db, rendered by
// namespace like every other file cloud opens), the "Prod = SQLite per tenant" rule
// (HIP-0302), or, with CLOUD_SQL_DSN set, a schema on hanzo-sql. Stores open lazily
// on first use, migrate once (the subsystem's Migrate), run the optional OnOpen
// seed, and are pooled (LRU-capped, idle-evicted).
//
// mu guards the pool's books and nothing slower: it is never held while a store
// opens. An open is the entry's own (a schema made and migrated over the network can
// take as long as the database likes), so concurrent opens of the SAME tenant share
// the one open, different tenants open in parallel, and a tenant that is already open
// never waits on another's.
type stores struct {
	name    string
	dataDir string
	migrate func(ctx context.Context, tx *sql.Tx, d dialect.Dialect) error
	onOpen  func(ctx context.Context, tenant string, db *sql.DB) error

	maxOpen int
	idleTTL time.Duration

	stop  context.Context
	halt  context.CancelFunc
	swept chan struct{}

	mu sync.Mutex
	// m is keyed by the NAMESPACE, which is what names the file: two spellings
	// that render one database cannot become two pooled handles on it.
	m   map[namespace.Namespace]*entry
	lru *list.List // *entry, front = most-recently-used, back = LRU
}

// entry is one pooled tenant DB. inUse counts in-flight dispatches holding it; an
// entry is evictable only when inUse==0 (never close a DB mid-transaction).
//
// An entry is in the pool from the moment its open starts, pinned by the caller that
// started it, so a second caller for the same tenant finds it and waits on ready
// instead of opening again. ready is closed when the open ends, db is set (under mu)
// when it succeeded and err when it did not; a failed entry leaves the pool, so the
// next caller opens afresh and a failure is never kept.
type entry struct {
	ns       namespace.Namespace
	db       *sql.DB
	err      error
	ready    chan struct{}
	inUse    int
	lastUsed time.Time
	el       *list.Element
}

// openTimeout bounds one open (the store, its migration and its seed). It is not the
// first caller's context: that caller may give up while others wait on the same open.
const openTimeout = time.Minute

func newStores(name, dataDir string, migrate func(context.Context, *sql.Tx, dialect.Dialect) error, onOpen func(context.Context, string, *sql.DB) error) *stores {
	stop, halt := context.WithCancel(context.Background())
	s := &stores{
		name:    name,
		dataDir: dataDir,
		migrate: migrate,
		onOpen:  onOpen,
		maxOpen: env.Int("CLOUD_GOJABASE_MAX_DBS", defaultMaxOpen),
		idleTTL: time.Duration(env.Int("CLOUD_GOJABASE_IDLE_TTL_SEC", int(defaultIdleTTL/time.Second))) * time.Second,
		stop:    stop,
		halt:    halt,
		m:       make(map[namespace.Namespace]*entry),
		lru:     list.New(),
	}
	if locus.DSN() != "" && locus.Ported(name) {
		if mode, err := locus.MoveMode(); err == nil && mode == locus.Sweep {
			s.swept = make(chan struct{})
			go func() {
				defer close(s.swept)
				s.sweep()
			}()
		}
	}
	return s
}

// acquire returns the tenant's *sql.DB (opening+migrating+seeding it on first use)
// PINNED for one dispatch, plus a release func the caller MUST call when the
// dispatch (and its transaction) finishes. While pinned (inUse>0) the entry is
// never evicted, so the returned handle stays valid for the whole request even if
// the pool is over capacity. Concurrent acquires of the same tenant share the one
// open; a caller waiting on it gives up when its own context ends.
func (s *stores) acquire(ctx context.Context, tenant string) (*sql.DB, func(), error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, nil, fmt.Errorf("gojabase[%s]: empty tenant", s.name)
	}
	ns, err := principal.OrgNamespace(tenant, "")
	if err != nil {
		return nil, nil, fmt.Errorf("gojabase[%s]: %w", s.name, err)
	}

	s.mu.Lock()
	s.sweepIdleLocked(time.Now())

	if e, ok := s.m[ns]; ok {
		e.inUse++
		e.lastUsed = time.Now()
		s.lru.MoveToFront(e.el)
		s.mu.Unlock()
		return s.await(ctx, e)
	}

	// Make room: close idle (inUse==0) LRU tails down toward the cap. If every
	// entry is pinned we exceed the cap transiently rather than close a live DB.
	s.evictToCapLocked()

	e := &entry{ns: ns, inUse: 1, lastUsed: time.Now(), ready: make(chan struct{})}
	e.el = s.lru.PushFront(e)
	s.m[ns] = e
	s.mu.Unlock()

	s.open(ctx, e, tenant)
	return s.await(ctx, e)
}

// await returns the entry's handle once its open has ended, pinned, or the open's
// error. The caller already holds a pin, which a failure gives back.
func (s *stores) await(ctx context.Context, e *entry) (*sql.DB, func(), error) {
	select {
	case <-e.ready:
	default:
		select {
		case <-e.ready:
		case <-ctx.Done():
			s.releaser(e)()
			return nil, nil, ctx.Err()
		}
	}
	if e.err != nil {
		s.releaser(e)()
		return nil, nil, e.err
	}
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

// open runs the entry's open outside mu and ends it: db on success, err and the
// entry's removal from the pool on failure, ready closed either way. A pool that was
// closed while the open ran no longer holds the entry, and the handle it made is
// closed rather than left behind.
func (s *stores) open(ctx context.Context, e *entry, tenant string) {
	octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openTimeout)
	defer cancel()
	db, err := s.openDB(octx, e.ns, tenant)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && s.m[e.ns] != e {
		_ = db.Close()
		db, err = nil, fmt.Errorf("gojabase[%s]: closed", s.name)
	}
	if err != nil {
		e.err = err
		if s.m[e.ns] == e {
			s.lru.Remove(e.el)
			delete(s.m, e.ns)
		}
	} else {
		e.db = db
	}
	close(e.ready)
}

// openDB opens+pragma+migrates+seeds a tenant DB. It takes the namespace (which names
// the file) AND the raw tenant (which the OnOpen seed is written in terms of).
func (s *stores) openDB(ctx context.Context, ns namespace.Namespace, tenant string) (*sql.DB, error) {
	// One connection to a file serializes writes against the single-writer file; the
	// per-request transaction (see Dispatch) then holds that one connection for the
	// whole dispatch, so a request is atomic. On hanzo-sql Dialect.BeginWrite takes
	// the same role.
	initDB := func(db *sql.DB) (*sql.DB, error) {
		if s.migrate != nil {
			if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
				return s.migrate(ctx, tx, dialect.Of(db))
			}); err != nil {
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
	if locus.DSN() != "" && locus.Ported(s.name) {
		db, err := locus.Use(ctx, nil, ns, s.name, s.dataDir, initDB)
		if err != nil {
			return nil, fmt.Errorf("gojabase[%s]: open %s: %w", s.name, ns, err)
		}
		return db, nil
	}
	db, err := cloud.OrgDB(s.dataDir, ns, s.name)
	if err != nil {
		return nil, fmt.Errorf("gojabase[%s]: open %s: %w", s.name, ns, err)
	}
	return initDB(db)
}

// sweep walks existing tenant SQLite files for this goja subsystem smallest-first
// when CLOUD_SQL_MOVE=sweep is set, moving each unmoved org file to hanzo-sql under
// the deployment's shared move slot so pre-existing tenants do not remain on disk.
func (s *stores) sweep() {
	root := filepath.Join(s.dataDir, "orgs")
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type diskOrg struct {
		ns   namespace.Namespace
		slug string
		size int64
	}
	var todo []diskOrg
	for _, e := range ents {
		if s.stop.Err() != nil {
			return
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		slug := e.Name()
		ns, err := principal.OrgOnDisk(slug)
		if err != nil {
			continue
		}
		path, err := namespace.Path(s.dataDir, ns, s.name)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if place, err := locus.Where(ns, s.name, s.dataDir); err == nil && place == locus.OnFile {
			todo = append(todo, diskOrg{ns: ns, slug: slug, size: info.Size()})
		}
	}
	if len(todo) == 0 {
		return
	}
	sort.Slice(todo, func(i, j int) bool {
		if todo[i].size != todo[j].size {
			return todo[i].size < todo[j].size
		}
		return todo[i].slug < todo[j].slug
	})
	releaseSlot, err := locus.Slot(s.stop)
	if err != nil {
		return
	}
	defer releaseSlot()
	for _, f := range todo {
		if s.stop.Err() != nil {
			return
		}
		_, rel, err := s.acquire(s.stop, f.slug)
		if err == nil {
			rel()
			// Release unpinned sweep handles immediately so a large sweep holds one
			// connection at a time instead of pinning maxOpen pools.
			s.mu.Lock()
			if e, ok := s.m[f.ns]; ok && e.inUse == 0 {
				s.closeEntryLocked(e)
			}
			s.mu.Unlock()
		}
	}
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
	if e.db != nil {
		_ = e.db.Close()
	}
	s.lru.Remove(e.el)
	delete(s.m, e.ns)
}

// closeAll stops any in-flight move sweep, closes every open tenant DB, and clears
// the pool. Idempotent. Called at shutdown, so it closes even pinned entries (no
// dispatch survives shutdown).
func (s *stores) closeAll() error {
	if s.halt != nil {
		s.halt()
	}
	if s.swept != nil {
		<-s.swept
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, e := range s.m {
		if e.db == nil {
			continue // still opening: open sees the pool no longer holds it and closes what it made
		}
		if err := e.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.m = make(map[namespace.Namespace]*entry)
	s.lru.Init()
	return firstErr
}
