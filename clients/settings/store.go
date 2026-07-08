package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags: cgo → mattn+SQLCipher;
	// !cgo → pure-Go modernc). Blank import registers the driver — same as
	// clients/eval and clients/integrations.
	_ "github.com/hanzoai/sqlite"
)

// The settings STORE is one SQLite file ({DataDir}/settings.db) holding every
// org's per-product configuration DOCUMENT. Tenancy is the composite primary key
// (org, product); EVERY query carries `WHERE org=? AND product=?`, so one org can
// never read or overwrite another's config. Secret VALUES are NEVER stored here —
// only the non-secret config values plus a per-secret "is set" marker; the secret
// material lives in KMS (see settings.go). The org column is the trust boundary at
// the query layer.

// Record is one org's persisted settings for one product.
type Record struct {
	Org       string
	Product   string
	Doc       string // opaque JSON config document (non-secret values + secret markers)
	UpdatedAt int64
}

// Store is the settings metastore over one SQLite file. MaxOpenConns(1)
// serializes writes against the file lock (same discipline as eval/integrations).
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS settings (
  org        TEXT NOT NULL,
  product    TEXT NOT NULL,
  doc        TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, product)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Get returns the persisted record for (org, product). found=false (nil error)
// when there is no row. Tenant isolation: the WHERE always binds org AND product.
func (s *Store) Get(ctx context.Context, org, product string) (Record, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org, product, doc, updated_at FROM settings WHERE org=? AND product=?`, org, product)
	var r Record
	err := row.Scan(&r.Org, &r.Product, &r.Doc, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("get settings: %w", err)
	}
	return r, true, nil
}

// Put upserts the config document for (org, product). The org is stamped from the
// validated principal by the caller; this never trusts a client-supplied org.
func (s *Store) Put(ctx context.Context, org, product, doc string, updatedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (org, product, doc, updated_at) VALUES (?,?,?,?)
		 ON CONFLICT(org, product) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		org, product, doc, updatedAt)
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	return nil
}
