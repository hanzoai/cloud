package prefs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// The prefs STORE is the durable half of the per-USER preference plane: one JSON
// document per user, following the settings-store discipline — Hanzo Base/SQLite,
// MaxOpenConns(1) to serialize writes against the file lock, one file.
//
// Isolation is the `subject` PRIMARY KEY and a mandatory `WHERE subject=?` on
// EVERY statement. The value is the VALIDATED principal (principal.Subject) —
// never normalized (casing/trimming would collapse distinct subjects into one
// bucket) and never a client-supplied header. A user's preferences are readable
// and writable by that user alone; there is deliberately no admin read path,
// because there is no operational reason to read someone's theme.
//
// NO SECRETS. Preferences are UI state — a theme, a density, a pinned nav. Unlike
// settings there is no KMS split to maintain, because nothing here is a
// credential. If a preference ever needs custody, it does not belong in this table.
type Store struct {
	db *sql.DB
}

// Prefs is one user's preference document.
type Prefs struct {
	Subject   string // the validated principal; the tenancy key
	Doc       string // opaque JSON object, stored verbatim (bounded at the edge)
	UpdatedAt int64
}

var errNotFound = errors.New("prefs: not found")

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
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
CREATE TABLE IF NOT EXISTS prefs (
  subject    TEXT NOT NULL PRIMARY KEY,
  doc        TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Get returns the persisted document for subject, or errNotFound when that user
// has never written one (the caller then serves an empty document, not a 404 —
// a preferences read always succeeds).
func (s *Store) Get(ctx context.Context, subject string) (Prefs, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT subject, doc, updated_at FROM prefs WHERE subject=?`, subject)
	var p Prefs
	err := row.Scan(&p.Subject, &p.Doc, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Prefs{}, errNotFound
	}
	if err != nil {
		return Prefs{}, fmt.Errorf("get prefs: %w", err)
	}
	return p, nil
}

// Merge applies a shallow key-wise merge of patch onto the user's stored document
// and returns the result, all inside ONE transaction on the single serialized
// connection.
//
// The merge is here rather than in the handler because read-modify-write across
// two calls is a lost update: two tabs saving different keys would race, and the
// later write would silently drop the earlier one's key. Doing it under the
// transaction makes concurrent saves of DIFFERENT keys both survive — which is
// exactly the multi-tab case a preferences surface actually sees.
func (s *Store) Merge(ctx context.Context, subject string, patch map[string]any, now int64) (Prefs, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Prefs{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored string
	switch err := tx.QueryRowContext(ctx, `SELECT doc FROM prefs WHERE subject=?`, subject).Scan(&stored); {
	case errors.Is(err, sql.ErrNoRows):
		stored = "{}"
	case err != nil:
		return Prefs{}, fmt.Errorf("lookup prefs: %w", err)
	}

	merged, err := mergeDoc(stored, patch)
	if err != nil {
		return Prefs{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO prefs (subject, doc, updated_at) VALUES (?,?,?)
		 ON CONFLICT(subject) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		subject, merged, now); err != nil {
		return Prefs{}, fmt.Errorf("put prefs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Prefs{}, fmt.Errorf("commit: %w", err)
	}
	return Prefs{Subject: subject, Doc: merged, UpdatedAt: now}, nil
}
