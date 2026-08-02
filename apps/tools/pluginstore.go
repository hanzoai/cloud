package tools

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/basedb"
	"github.com/hanzoai/namespace"
)

// AuthoredPlugin is one org-authored connector plugin: the TypeScript a person
// (or the generator) wrote, plus the CommonJS the bundler produced from it.
//
// It holds NO credential. A plugin declares WHICH connector provider it needs
// and the credential stays in the connectors plane, where it is already under
// KMS custody — so a generated plugin is safe to read, diff, and re-bundle, and
// rotating a key never means editing code.
type AuthoredPlugin struct {
	// ID is the plugin's id within the org, and the id a delete addresses.
	ID string `json:"id"`
	// Org is the org that built the plugin — the validated caller's.
	Org string `json:"org"`
	// Name is the plugin's name: one lowercase path segment, the id it runs by.
	Name string `json:"name"`
	// Provider is the connectors provider whose credential this plugin uses at
	// run time. Absent for a plugin that needs none. The credential itself is
	// never here — it stays under KMS custody in the connectors plane.
	Provider string `json:"provider,omitempty"`
	// Source is the TypeScript as authored (or as generated from a spec).
	Source string `json:"source"`
	// Bundled is the CommonJS the bundler produced, which the runtime executes.
	// Never rendered to a client.
	Bundled string `json:"-"`
	// CreatedAt is when the plugin was last built, Unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// AuthoredStore is the per-org registry of authored plugins (one SQLite file,
// org column, the shared cloud store discipline). Isolation is a mandatory
// `WHERE org=?` on every statement; org is the validated principal value.
type AuthoredStore struct {
	db *sql.DB
}

// OpenAuthoredStore opens (and migrates) the authored-plugin store under dir.
func OpenAuthoredStore(dir string) (*AuthoredStore, error) {
	db, err := basedb.Open(namespace.System(), "tools-plugins", dir)
	if err != nil {
		return nil, fmt.Errorf("tools: open authored-plugin store: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tools: authored-plugin pragma %q: %w", pragma, err)
		}
	}
	s := &AuthoredStore{db: db}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS authored_plugins (
  id         TEXT NOT NULL,
  org        TEXT NOT NULL,
  name       TEXT NOT NULL,
  provider   TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL,
  bundled    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org, id)
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: authored-plugin migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database.
func (s *AuthoredStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Put inserts or replaces a plugin under (org, name). Re-building the same name
// supersedes it rather than accumulating versions: the store answers "what does
// this org run now", and a build that is not the current one has no reader.
func (s *AuthoredStore) Put(ctx context.Context, p AuthoredPlugin) (AuthoredPlugin, error) {
	if p.Org == "" {
		return AuthoredPlugin{}, fmt.Errorf("tools: authored plugin: empty org")
	}
	if p.ID == "" {
		p.ID = "p" + randHex(6)
	}
	p.CreatedAt = time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO authored_plugins (id, org, name, provider, source, bundled, created_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(org, id) DO UPDATE SET
		   name=excluded.name, provider=excluded.provider,
		   source=excluded.source, bundled=excluded.bundled, created_at=excluded.created_at`,
		p.ID, p.Org, p.Name, p.Provider, p.Source, p.Bundled, p.CreatedAt); err != nil {
		return AuthoredPlugin{}, fmt.Errorf("tools: put authored plugin: %w", err)
	}
	return p, nil
}

// List returns the org's authored plugins, newest first. Bundled is omitted from
// JSON by its tag, so a list response carries the source but not the artifact.
func (s *AuthoredStore) List(ctx context.Context, org string) ([]AuthoredPlugin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, provider, source, created_at FROM authored_plugins
		 WHERE org=? ORDER BY created_at DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("tools: list authored plugins: %w", err)
	}
	defer rows.Close()
	out := []AuthoredPlugin{}
	for rows.Next() {
		var p AuthoredPlugin
		if err := rows.Scan(&p.ID, &p.Org, &p.Name, &p.Provider, &p.Source, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("tools: scan authored plugin: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one plugin INCLUDING its bundled artifact — the runtime needs it.
func (s *AuthoredStore) Get(ctx context.Context, org, id string) (AuthoredPlugin, error) {
	var p AuthoredPlugin
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, name, provider, source, bundled, created_at FROM authored_plugins
		 WHERE org=? AND id=?`, org, id).
		Scan(&p.ID, &p.Org, &p.Name, &p.Provider, &p.Source, &p.Bundled, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return AuthoredPlugin{}, sql.ErrNoRows
	}
	if err != nil {
		return AuthoredPlugin{}, fmt.Errorf("tools: get authored plugin: %w", err)
	}
	return p, nil
}

// Delete removes one of the org's plugins. Deleting what is not there is not an
// error — the caller's intent is "gone", and it is.
func (s *AuthoredStore) Delete(ctx context.Context, org, id string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM authored_plugins WHERE org=? AND id=?`, org, id); err != nil {
		return fmt.Errorf("tools: delete authored plugin: %w", err)
	}
	return nil
}
