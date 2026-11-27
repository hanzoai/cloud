package environment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// Store is one org's environments: a row per codebase, in the org's own database
// ({DataDir}/orgs/<org>/environment.db, or a schema on hanzo-sql), so isolation is
// the store and no statement can read across it. Its SQL is the dialect both
// engines share (sdk/dialect).
type Store struct{ db *sql.DB }

// row is one codebase's environment as the file holds it.
type row struct {
	repo, install, start, session string
	secrets                       []string
	proposal                      *Proposal
	updated                       int64
}

func openStore(db *sql.DB) (*Store, error) {
	d := dialect.Of(db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	const ddl = `
CREATE TABLE IF NOT EXISTS environments (
  repo       TEXT NOT NULL PRIMARY KEY,
  install    TEXT NOT NULL DEFAULT '',
  start      TEXT NOT NULL DEFAULT '',
  secrets    TEXT NOT NULL DEFAULT '[]',
  proposal   TEXT NOT NULL DEFAULT '',
  session    TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL
);`
	if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}); err != nil {
		return nil, fmt.Errorf("environment: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the org's file.
func (s *Store) Close() error { return s.db.Close() }

const columns = `repo, install, start, secrets, proposal, session, updated_at`

func scan(from func(...any) error) (row, error) {
	var r row
	var secrets, proposal string
	if err := from(&r.repo, &r.install, &r.start, &secrets, &proposal, &r.session, &r.updated); err != nil {
		return row{}, err
	}
	_ = json.Unmarshal([]byte(secrets), &r.secrets)
	if proposal != "" {
		var p Proposal
		if json.Unmarshal([]byte(proposal), &p) == nil {
			r.proposal = &p
		}
	}
	return r, nil
}

func (s *Store) list(ctx context.Context) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM environments ORDER BY repo`)
	if err != nil {
		return nil, fmt.Errorf("environment: list: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scan(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("environment: list: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// get answers the codebase's row, and false for a codebase with none.
func (s *Store) get(ctx context.Context, repo string) (row, bool, error) {
	if dialect.Text(repo) != nil {
		return row{repo: repo}, false, nil // nothing is kept under text the store refuses
	}
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM environments WHERE repo=$1`, repo).Scan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return row{repo: repo}, false, nil
	case err != nil:
		return row{}, false, fmt.Errorf("environment: get: %w", err)
	}
	return r, true, nil
}

// change reads a codebase's row, lets edit change it, and writes it back in one
// transaction — so a secret set while the scripts are saved is never lost to the
// other write.
func (s *Store) change(ctx context.Context, repo string, now int64, edit func(*row)) (row, error) {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return row{}, fmt.Errorf("environment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM environments WHERE repo=$1`, repo).Scan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		r = row{repo: repo}
	case err != nil:
		return row{}, fmt.Errorf("environment: read: %w", err)
	}
	edit(&r)
	r.updated = now
	secrets, _ := json.Marshal(names(r.secrets))
	proposal := ""
	if r.proposal != nil {
		b, _ := json.Marshal(r.proposal)
		proposal = string(b)
	}
	if err := dialect.Text(repo, r.install, r.start, string(secrets), proposal, r.session); err != nil {
		return row{}, fmt.Errorf("environment: write: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO environments (repo, install, start, secrets, proposal, session, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(repo) DO UPDATE SET install=excluded.install, start=excluded.start, secrets=excluded.secrets,
		   proposal=excluded.proposal, session=excluded.session, updated_at=excluded.updated_at`,
		repo, r.install, r.start, string(secrets), proposal, r.session, r.updated); err != nil {
		return row{}, fmt.Errorf("environment: write: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return row{}, fmt.Errorf("environment: commit: %w", err)
	}
	return r, nil
}

func (s *Store) forget(ctx context.Context, repo string) error {
	if dialect.Text(repo) != nil {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE repo=$1`, repo); err != nil {
		return fmt.Errorf("environment: forget: %w", err)
	}
	return nil
}

// names is s, never nil, so the wire says [] rather than null.
func names(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
