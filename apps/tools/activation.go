package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
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
//
// A PERSON'S LAYER sits on top and only narrows (muted): a name one person
// switched off of the org's kit for their own runs. It can never add a name — the
// kit is the admin's rows minus the person's — so it is the person's to write.
//
// A row also says whether an ADMIN of the org (or a SuperAdmin) switched it on.
// Dispatch reads every row; an agent run reads only those (kit.go), because what
// a run carries is loaded into every colleague's run with its tools approved in
// advance, so it is what an admin put in place and nothing a member did. A row
// written before the column existed says no, and counts for a run again once an
// admin switches it on.
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

// OpenActivationStore opens (and migrates) the activation store under dir.
func OpenActivationStore(dir string) (*ActivationStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "tools-activation", dir, func(db *sql.DB) (*ActivationStore, error) {
		s := &ActivationStore{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *ActivationStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS activation (
  org        TEXT NOT NULL,
  project    TEXT NOT NULL,
  tool       TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT '',
  by_user    TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  PRIMARY KEY (org, project, tool)
);
CREATE INDEX IF NOT EXISTS ix_activation_scope ON activation(org, project);
CREATE TABLE IF NOT EXISTS muted (
  org        TEXT NOT NULL,
  person     TEXT NOT NULL,
  tool       TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (org, person, tool)
);`
	ctx := context.Background()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		// A store made before the column existed gains it here, once.
		has, err := d.HasColumn(ctx, tx, "activation", "admitted")
		if err != nil || has {
			return err
		}
		_, err = tx.ExecContext(ctx, `ALTER TABLE activation ADD COLUMN admitted BIGINT NOT NULL DEFAULT 0`)
		return err
	}); err != nil {
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
		`SELECT 1 FROM activation WHERE org=$1 AND project=$2 AND tool=$3`,
		org, projectKey(project), tool).Scan(&one)
	return err == nil
}

// List returns the activated tool names for (org, project), sorted.
func (s *ActivationStore) List(ctx context.Context, org, project string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tool FROM activation WHERE org=$1 AND project=$2 ORDER BY tool`,
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

// Admitted returns the tool names an admin activated for (org, project), sorted:
// the set an agent run carries.
func (s *ActivationStore) Admitted(ctx context.Context, org, project string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tool FROM activation WHERE org=$1 AND project=$2 AND admitted=1 ORDER BY tool`,
		org, projectKey(project))
	if err != nil {
		return nil, fmt.Errorf("tools: activation admitted: %w", err)
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
// source + byUser are recorded for the audit/console view, and admitted says
// whether the caller administers the org — the latest write decides it.
func (s *ActivationStore) Activate(ctx context.Context, org, project, tool string, source Source, byUser string, admitted bool) error {
	if s == nil || s.db == nil {
		return errors.New("tools: activation store not configured")
	}
	if err := dialect.Text(org, project, tool, string(source), byUser); err != nil {
		return fmt.Errorf("tools: activate: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO activation (org, project, tool, source, by_user, created_at, admitted)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(org, project, tool) DO UPDATE SET source=excluded.source, by_user=excluded.by_user, admitted=excluded.admitted`,
		org, projectKey(project), tool, string(source), byUser, time.Now().Unix(), boolInt(admitted))
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
		`DELETE FROM activation WHERE org=$1 AND project=$2 AND tool=$3`,
		org, projectKey(project), tool)
	if err != nil {
		return fmt.Errorf("tools: deactivate: %w", err)
	}
	return nil
}

// Muted returns the names one person switched off of the org's kit, sorted. An
// empty person has no layer.
func (s *ActivationStore) Muted(ctx context.Context, org, person string) ([]string, error) {
	if s == nil || s.db == nil || org == "" || person == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tool FROM muted WHERE org=$1 AND person=$2 ORDER BY tool`, org, person)
	if err != nil {
		return nil, fmt.Errorf("tools: muted: %w", err)
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

// Mute switches name off of one person's runs. Idempotent.
func (s *ActivationStore) Mute(ctx context.Context, org, person, tool string) error {
	if s == nil || s.db == nil || org == "" || person == "" {
		return errors.New("tools: mute needs an org and a person")
	}
	if err := dialect.Text(org, person, tool); err != nil {
		return fmt.Errorf("tools: mute: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO muted (org, person, tool, created_at) VALUES ($1,$2,$3,$4) ON CONFLICT(org, person, tool) DO NOTHING`,
		org, person, tool, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("tools: mute: %w", err)
	}
	return nil
}

// Unmute gives name back to one person's runs, as far as the org carries it.
// Idempotent.
func (s *ActivationStore) Unmute(ctx context.Context, org, person, tool string) error {
	if s == nil || s.db == nil || org == "" || person == "" {
		return errors.New("tools: unmute needs an org and a person")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM muted WHERE org=$1 AND person=$2 AND tool=$3`, org, person, tool); err != nil {
		return fmt.Errorf("tools: unmute: %w", err)
	}
	return nil
}
