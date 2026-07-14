package company

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when an org has no formation yet. Handlers map it to 404.
var errNotFound = errors.New("company: formation not found")

// Store persists formations. ONE SQLite file ({DataDir}/company.db) holds every
// org's formation; tenant isolation is the `org` primary key, enforced on EVERY
// query. There is at most one formation per org (an org forms one company through
// this flow), so the aggregate is stored as a single row: the machine-relevant
// projection (stage/structure/name) in columns for cheap listing, and the full
// Formation as a JSON document in `data`. MaxOpenConns(1) serializes writes.
type Store struct {
	db *sql.DB
}

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

// migrate creates the formations table. Idempotent (IF NOT EXISTS). `org` is the
// primary key, so tenant isolation is a physical property.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS company_formations (
  org         TEXT PRIMARY KEY,
  stage       TEXT NOT NULL,
  structure   TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_company_formations_stage ON company_formations(stage);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("company migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// Get loads the org's formation, or errNotFound.
func (s *Store) Get(ctx context.Context, org string) (*Formation, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM company_formations WHERE org=?`, org).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get formation: %w", err)
	}
	var f Formation
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		return nil, fmt.Errorf("decode formation: %w", err)
	}
	// The org column is authoritative — never trust the JSON's own copy.
	f.Org = org
	return &f, nil
}

// Put upserts the formation. The org, stage, structure, and name projections are
// written alongside the JSON so a list/summary never has to decode every row.
func (s *Store) Put(ctx context.Context, f *Formation) error {
	if f == nil || f.Org == "" {
		return fmt.Errorf("company: put requires a formation with an org")
	}
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode formation: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO company_formations (org, stage, structure, name, data, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(org) DO UPDATE SET
		   stage=excluded.stage, structure=excluded.structure, name=excluded.name,
		   data=excluded.data, updated_at=excluded.updated_at`,
		f.Org, string(f.Stage), string(f.Structure), f.Name, string(data), f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("put formation: %w", err)
	}
	return nil
}

// Delete removes the org's formation (used only in tests / a hard reset).
func (s *Store) Delete(ctx context.Context, org string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM company_formations WHERE org=?`, org)
	if err != nil {
		return false, fmt.Errorf("delete formation: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
