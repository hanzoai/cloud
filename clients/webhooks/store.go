package webhooks

// store.go — one per-org (HIP-0302 physically-isolated) SQLite holding the org's
// webhook endpoint registry. A distinct org resolves to a distinct
// {DataDir}/orgs/{slug}/webhooks.db, so one org can never read or mutate another's
// endpoints — the same per-org file idiom clients/books uses (cloud.OrgStore over
// cloud.OrgDB). Each org owns its file; there is no cross-org query surface.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// errNotFound is the store's one "no such row" sentinel; the API layer maps it to 404.
var errNotFound = errors.New("webhooks: endpoint not found")

// store wraps one org's *sql.DB. cloud.OrgStore opens (and migrates) it exactly once
// per org file and calls Close on shutdown.
type store struct {
	db *sql.DB
}

// openStore is the open func cloud.OrgStore calls once per org file: it wraps the
// already-pragma'd *sql.DB and runs the (idempotent) migration.
func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS endpoint (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  url         TEXT NOT NULL,
  events      TEXT NOT NULL DEFAULT '[]',
  secret      TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'active',
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS endpoint_status ON endpoint(status);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("webhooks migrate: %w", err)
	}
	return nil
}

// create inserts a new endpoint row. The caller has already minted id + secret and
// stamped created/updated.
func (s *store) create(ctx context.Context, e Endpoint) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO endpoint (id, org, url, events, secret, status, description, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Org, e.URL, encodeEvents(e.Events), e.Secret, e.Status, e.Description, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("webhooks create: %w", err)
	}
	return nil
}

// list returns every endpoint in this org's file (newest first).
func (s *store) list(ctx context.Context) ([]Endpoint, error) {
	return s.query(ctx, `SELECT id, org, url, events, secret, status, description, created_at, updated_at
		FROM endpoint ORDER BY created_at DESC, id DESC`)
}

// listActive returns only the org's ACTIVE endpoints — the set the dispatcher matches
// an event against. A disabled endpoint is never returned here, so it is never delivered.
func (s *store) listActive(ctx context.Context) ([]Endpoint, error) {
	return s.query(ctx, `SELECT id, org, url, events, secret, status, description, created_at, updated_at
		FROM endpoint WHERE status = 'active' ORDER BY id`)
}

func (s *store) query(ctx context.Context, q string, args ...any) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("webhooks query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// get returns one endpoint by id, or errNotFound.
func (s *store) get(ctx context.Context, id string) (Endpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org, url, events, secret, status, description, created_at, updated_at
		 FROM endpoint WHERE id = ?`, id)
	e, err := scan(row)
	if err == sql.ErrNoRows {
		return Endpoint{}, errNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks get: %w", err)
	}
	return e, nil
}

// update mutates the caller-editable fields (url, events, status, description) of an
// existing endpoint, stamping updated_at. The secret and created_at are immutable. It
// returns the stored row, or errNotFound.
func (s *store) update(ctx context.Context, id, url string, events []string, status, description, updatedAt string) (Endpoint, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE endpoint SET url = ?, events = ?, status = ?, description = ?, updated_at = ? WHERE id = ?`,
		url, encodeEvents(events), status, description, updatedAt, id)
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Endpoint{}, errNotFound
	}
	return s.get(ctx, id)
}

// del removes an endpoint, reporting whether a row was deleted.
func (s *store) del(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM endpoint WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("webhooks delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// scanner abstracts *sql.Row and *sql.Rows so scan serves both get and query.
type scanner interface {
	Scan(dest ...any) error
}

func scan(r scanner) (Endpoint, error) {
	var e Endpoint
	var events string
	if err := r.Scan(&e.ID, &e.Org, &e.URL, &events, &e.Secret, &e.Status, &e.Description, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Endpoint{}, err
	}
	e.Events = decodeEvents(events)
	return e, nil
}

// encodeEvents serializes the subject-pattern list to the stored JSON text. A nil or
// empty list stores "[]" (which decodes back to an empty slice → "all events").
func encodeEvents(events []string) string {
	if len(events) == 0 {
		return "[]"
	}
	b, err := json.Marshal(events)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeEvents parses the stored JSON text back to the pattern list. A malformed or
// empty value decodes to an empty slice (matches everything), never an error.
func decodeEvents(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}
