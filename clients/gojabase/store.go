package gojabase

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest; !cgo → pure-Go modernc). Blank import registers it. This
	// is the SAME driver clients/crm, clients/prompts, clients/eval use — one
	// storage substrate for every Base-backed subsystem.
	_ "github.com/hanzoai/sqlite"
)

// stores is the per-tenant SQLite manager: ONE database FILE per tenant
// ({DataDir}/{name}/{tenantSlug}.db), the "Prod = SQLite per tenant" rule
// (HIP-0302). Files open lazily on first use, migrate once (the subsystem's
// Schema DDL), run the optional OnOpen seed, and are cached for the process
// lifetime. Concurrent opens of the same tenant are single-flighted under mu.
type stores struct {
	name   string
	dir    string
	schema string
	onOpen func(ctx context.Context, tenant string, db *sql.DB) error

	mu sync.Mutex
	m  map[string]*sql.DB
}

func newStores(name, dataDir, schema string, onOpen func(context.Context, string, *sql.DB) error) *stores {
	return &stores{
		name:   name,
		dir:    filepath.Join(dataDir, name),
		schema: schema,
		onOpen: onOpen,
		m:      make(map[string]*sql.DB),
	}
}

// get returns the tenant's *sql.DB, opening+migrating+seeding it on first use.
func (s *stores) get(ctx context.Context, tenant string) (*sql.DB, error) {
	slug := slugify(tenant)
	if slug == "" {
		return nil, fmt.Errorf("gojabase[%s]: empty tenant", s.name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.m[slug]; ok {
		return db, nil
	}

	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, fmt.Errorf("gojabase[%s]: mkdir %q: %w", s.name, s.dir, err)
	}
	path := filepath.Join(s.dir, slug+".db")
	db, err := sql.Open("sqlite", path)
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
	s.m[slug] = db
	return db, nil
}

// closeAll closes every open tenant DB. Idempotent.
func (s *stores) closeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for slug, db := range s.m {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.m, slug)
	}
	return firstErr
}

// slugify maps a tenant/org key to a safe single-path-segment filename. The org
// is the validated IAM owner (a short DNS-ish label); anything outside
// [a-z0-9._-] is replaced with '_' so it can never traverse directories or
// escape the subsystem's data subtree. Distinct orgs never collide because the
// mapping only rewrites illegal bytes (legal orgs pass through verbatim) — but a
// pathological collision is still contained to one file, never cross-tenant.
func slugify(tenant string) string {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(tenant) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	// Guard against '.'/'..' path tricks after normalization.
	if out == "." || out == ".." {
		return "_" + out
	}
	return out
}
