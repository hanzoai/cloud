package pref

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Open is the ONE opener: with CLOUD_SQL_DSN set the database is a schema
	// on hanzo-sql, and otherwise it is born encrypted under the key cek derives from
	// the process master and the system namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// The prefs STORE is the durable half of the per-USER preference plane: one JSON
// document per user, following the settings-store discipline — Hanzo Base/SQLite,
// one database for the deployment. Its SQL is the dialect both engines share
// (sdk/dialect) and its tests run on both (internal/sqltest).
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
	d  dialect.Dialect
}

// Prefs is one user's preference document.
type Prefs struct {
	Subject   string // the validated principal; the tenancy key
	Doc       string // opaque JSON object, stored verbatim (bounded at the edge)
	UpdatedAt int64
}

var errNotFound = errors.New("prefs: not found")

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "prefs", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db)}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS prefs (
  subject    TEXT NOT NULL PRIMARY KEY,
  doc        TEXT NOT NULL DEFAULT '{}',
  updated_at BIGINT NOT NULL
)`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
		return s, nil
	})
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Get returns the persisted document for subject, or errNotFound when that user
// has never written one (the caller then serves an empty document, not a 404 —
// a preferences read always succeeds).
func (s *Store) Get(ctx context.Context, subject string) (Prefs, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT subject, doc, updated_at FROM prefs WHERE subject=$1`, subject)
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
// and returns the result, all inside ONE transaction under the store's write lock.
//
// The merge is here rather than in the handler because read-modify-write across
// two calls is a lost update: two tabs saving different keys would race, and the
// later write would silently drop the earlier one's key. Doing it under the
// transaction makes concurrent saves of DIFFERENT keys both survive — which is
// exactly the multi-tab case a preferences surface actually sees.
func (s *Store) Merge(ctx context.Context, subject string, patch map[string]any, now int64) (Prefs, error) {
	if err := dialect.Text(subject); err != nil {
		return Prefs{}, fmt.Errorf("prefs subject: %w", err)
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Prefs{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored string
	switch err := tx.QueryRowContext(ctx, `SELECT doc FROM prefs WHERE subject=$1`, subject).Scan(&stored); {
	case errors.Is(err, sql.ErrNoRows):
		stored = "{}"
	case err != nil:
		return Prefs{}, fmt.Errorf("lookup prefs: %w", err)
	}

	merged, err := mergeDoc(stored, patch)
	if err != nil {
		return Prefs{}, err
	}
	if err := dialect.Text(merged); err != nil {
		return Prefs{}, fmt.Errorf("prefs doc: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO prefs (subject, doc, updated_at) VALUES ($1,$2,$3)
		 ON CONFLICT(subject) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		subject, merged, now); err != nil {
		return Prefs{}, fmt.Errorf("put prefs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Prefs{}, fmt.Errorf("commit: %w", err)
	}
	return Prefs{Subject: subject, Doc: merged, UpdatedAt: now}, nil
}
