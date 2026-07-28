package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/cek"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (see the same
	// blank import in clients/projects/store.go for why it must not be modernc).
	_ "github.com/hanzoai/sqlite"
)

// errConflict is returned when the caller's org already owns that slug.
var errConflict = errors.New("templates: slug taken")

// Store holds every org's PRIVATE templates — ONE SQLite file
// ({DataDir}/templates.db) whose isolation key is the org column, the same
// discipline clients/projects and clients/marketplace keep.
//
// The PUBLIC catalog is deliberately NOT in this table: it stays the embedded
// catalog.json, which has no write route at all. So a private row has no path
// into the public gallery by CONSTRUCTION, not by a filter someone has to
// remember to write — the two live in different containers, and every read of
// this one binds org.
//
// The whole Template is one JSON `doc`: it is already the exact shape the API
// serves, so a row can never drift from it as fields are added, and (org, slug)
// — the only two things ever queried on — stay real columns.
type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("templates: open store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("templates: pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS org_templates (
  org        TEXT NOT NULL,
  slug       TEXT NOT NULL,
  doc        TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, slug)
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("templates: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Put writes t for its own org. create=true inserts and reports errConflict if
// the org already holds that slug; create=false replaces an existing row and
// reports sql.ErrNoRows if there is none — so "publish" can never silently
// clobber and "edit" can never silently create.
func (s *Store) Put(ctx context.Context, t Template, create bool, now int64) error {
	doc, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("templates: encode %q: %w", t.Slug, err)
	}
	if !create {
		res, err := s.db.ExecContext(ctx,
			`UPDATE org_templates SET doc=?, updated_at=? WHERE org=? AND slug=?`, doc, now, t.Org, t.Slug)
		if err != nil {
			return fmt.Errorf("templates: update %q: %w", t.Slug, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO org_templates (org,slug,doc,created_at,updated_at) VALUES (?,?,?,?,?)`,
		t.Org, t.Slug, doc, now, now); err != nil {
		if isUnique(err) {
			return errConflict
		}
		return fmt.Errorf("templates: insert %q: %w", t.Slug, err)
	}
	return nil
}

// Get returns org's own template at slug. found=false (nil error) when absent —
// including when the row belongs to ANOTHER org, because the WHERE binds org.
func (s *Store) Get(ctx context.Context, org, slug string) (Template, bool, error) {
	var doc []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT doc FROM org_templates WHERE org=? AND slug=?`, org, slug).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, false, nil
	}
	if err != nil {
		return Template{}, false, fmt.Errorf("templates: get %q: %w", slug, err)
	}
	t, err := decode(doc, org)
	return t, err == nil, err
}

// List returns org's own templates (tenant-scoped read; there is no all-orgs
// counterpart, because nothing in this subsystem legitimately wants one).
func (s *Store) List(ctx context.Context, org string) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM org_templates WHERE org=? ORDER BY slug`, org)
	if err != nil {
		return nil, fmt.Errorf("templates: list: %w", err)
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("templates: list scan: %w", err)
		}
		t, err := decode(doc, org)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Delete removes org's own template at slug, reporting whether a row went.
func (s *Store) Delete(ctx context.Context, org, slug string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM org_templates WHERE org=? AND slug=?`, org, slug)
	if err != nil {
		return false, fmt.Errorf("templates: delete %q: %w", slug, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// decode restores a stored Template and re-stamps Org from the ROW's key, so the
// served owner is the column the query isolated on and never whatever a stale
// doc happens to carry.
func decode(doc []byte, org string) (Template, error) {
	var t Template
	if err := json.Unmarshal(doc, &t); err != nil {
		return Template{}, fmt.Errorf("templates: decode %q: %w", org, err)
	}
	t.Org = org
	if t.Features == nil {
		t.Features = []string{}
	}
	return t, nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
