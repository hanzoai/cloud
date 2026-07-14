package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// ActivationStore is the per-(org,project,tool) activation plane: the durable
// toggle hanzo.chat / hanzo.app read to decide which skills/plugins/connectors an
// org has turned on. It is the tool-plane twin of the entitlements (org,product)
// enablement store — a row exists iff the tool is EXPLICITLY activated for that
// scope, and dispatch fails closed (403) on any tool without one.
//
// Isolation is the (org, project, tool) composite key plus a mandatory
// `WHERE org=? AND project=?` on every statement. org/project are the VALIDATED
// principal values, never client input. One SQLite file (all orgs, org column),
// MaxOpenConns(1), WAL — the shared cloud store discipline.
type ActivationStore struct {
	db *sql.DB
}

// projectKey folds "" (default scope) and the literal default project to one key,
// so a tool activated at the org default is visible to the default-project caller.
func projectKey(project string) string {
	if project == "" || project == "default" {
		return "default"
	}
	return project
}

// OpenActivationStore opens (and migrates) the activation store at path.
func OpenActivationStore(path string) (*ActivationStore, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tools: open activation store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tools: activation pragma %q: %w", pragma, err)
		}
	}
	s := &ActivationStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *ActivationStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS activation (
  org        TEXT NOT NULL,
  project    TEXT NOT NULL,
  tool       TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT '',
  by_user    TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org, project, tool)
);
CREATE INDEX IF NOT EXISTS ix_activation_scope ON activation(org, project);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("tools: activation migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *ActivationStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// IsActivated reports whether tool is activated for (org, project). It is nil-safe:
// a nil store (pre-Mount / split deploy) reports NOT activated, so dispatch fails
// closed rather than serving an unactivated tool.
func (s *ActivationStore) IsActivated(ctx context.Context, org, project, tool string) bool {
	if s == nil || s.db == nil || org == "" {
		return false
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM activation WHERE org=? AND project=? AND tool=?`,
		org, projectKey(project), tool).Scan(&one)
	return err == nil
}

// List returns the activated tool names for (org, project), sorted.
func (s *ActivationStore) List(ctx context.Context, org, project string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tool FROM activation WHERE org=? AND project=? ORDER BY tool`,
		org, projectKey(project))
	if err != nil {
		return nil, fmt.Errorf("tools: activation list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Activate turns a tool on for (org, project). Idempotent on the composite key.
// source + byUser are recorded for the audit/console view.
func (s *ActivationStore) Activate(ctx context.Context, org, project, tool string, source Source, byUser string) error {
	if s == nil || s.db == nil {
		return errors.New("tools: activation store not configured")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO activation (org, project, tool, source, by_user, created_at)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org, project, tool) DO UPDATE SET source=excluded.source, by_user=excluded.by_user`,
		org, projectKey(project), tool, string(source), byUser, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("tools: activate: %w", err)
	}
	return nil
}

// Deactivate turns a tool off for (org, project). Idempotent (no row ⇒ no-op).
func (s *ActivationStore) Deactivate(ctx context.Context, org, project, tool string) error {
	if s == nil || s.db == nil {
		return errors.New("tools: activation store not configured")
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM activation WHERE org=? AND project=? AND tool=?`,
		org, projectKey(project), tool)
	if err != nil {
		return fmt.Errorf("tools: deactivate: %w", err)
	}
	return nil
}
