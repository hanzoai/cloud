package world

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	_ "github.com/hanzoai/sqlite"
)

// The world PIPELINE store is the durable, per-(org,project) config for the news
// data plane: the RSS feed URLs to poll plus the region/keyword/source filters
// applied to the merged result. It is the metastore twin of the settings store
// (clients/settings/store.go) and follows the same discipline — Hanzo Base/SQLite,
// MaxOpenConns(1) to serialize writes against the file lock, one file for every org.
//
// Tenant isolation is the (org, project) COMPOSITE PRIMARY KEY and a mandatory
// `WHERE org=? AND project=?` on EVERY statement. The org value is the validated
// owner claim (principal.Org, HIP-0026) — never normalized (casing/trimming
// collapses distinct owners into one bucket) and never a client-supplied header.
// The project is principal.Project — the org sub-scope — bound the same way.

var errNotFound = errors.New("world: pipeline not found")

// Filters narrows the merged news stream. Each axis is an OR-set of terms; a
// non-empty axis is an AND predicate against the item (empty axis = pass-through).
// Keywords ALSO seed the GDELT queries, so it is both a fetch input (fresh
// keyword-matched articles) and a post-merge filter.
type Filters struct {
	Regions  []string `json:"regions"`
	Keywords []string `json:"keywords"`
	Sources  []string `json:"sources"`
}

// Pipeline is the persisted per-(org,project) news pipeline config. Feeds are
// host-allowlisted RSS/Atom URLs; Filters narrows the merged result.
type Pipeline struct {
	Org       string
	Project   string
	Feeds     []string
	Filters   Filters
	CreatedAt int64
	UpdatedAt int64
}

// PipelineStore is the world pipeline metastore over one SQLite file
// ({DataDir}/world.db). Tenancy is the (org, project) key.
type PipelineStore struct {
	db *sql.DB
}

func openPipelineStore(path string) (*PipelineStore, error) {
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
	s := &PipelineStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PipelineStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS pipelines (
  org        TEXT NOT NULL,
  project    TEXT NOT NULL,
  feeds      TEXT NOT NULL DEFAULT '[]',
  filters    TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, project)
);
CREATE INDEX IF NOT EXISTS ix_pipelines_org ON pipelines(org, updated_at);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *PipelineStore) Close() error { return s.db.Close() }

// Get returns the persisted pipeline for (org, project), or errNotFound when the
// org has never written a pipeline for that project (the caller then serves the
// default pipeline).
func (s *PipelineStore) Get(ctx context.Context, org, project string) (Pipeline, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org, project, feeds, filters, created_at, updated_at
		   FROM pipelines WHERE org=? AND project=?`, org, project)
	var (
		p           Pipeline
		feedsJSON   string
		filtersJSON string
	)
	err := row.Scan(&p.Org, &p.Project, &feedsJSON, &filtersJSON, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Pipeline{}, errNotFound
	}
	if err != nil {
		return Pipeline{}, fmt.Errorf("get pipeline: %w", err)
	}
	p.Feeds = decodeFeeds(feedsJSON)
	p.Filters = decodeFilters(filtersJSON)
	return p, nil
}

// Put upserts the feeds + filters for (org, project). It is idempotent on the
// composite key and preserves created_at across updates.
func (s *PipelineStore) Put(ctx context.Context, p Pipeline) (Pipeline, error) {
	feedsJSON := encodeJSON(p.Feeds, "[]")
	filtersJSON := encodeJSON(p.Filters, "{}")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Pipeline{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM pipelines WHERE org=? AND project=?`, p.Org, p.Project)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		p.CreatedAt = p.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pipelines (org, project, feeds, filters, created_at, updated_at)
			 VALUES (?,?,?,?,?,?)`,
			p.Org, p.Project, feedsJSON, filtersJSON, p.CreatedAt, p.UpdatedAt); err != nil {
			return Pipeline{}, fmt.Errorf("insert pipeline: %w", err)
		}
	case err != nil:
		return Pipeline{}, fmt.Errorf("lookup pipeline: %w", err)
	default:
		p.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE pipelines SET feeds=?, filters=?, updated_at=? WHERE org=? AND project=?`,
			feedsJSON, filtersJSON, p.UpdatedAt, p.Org, p.Project); err != nil {
			return Pipeline{}, fmt.Errorf("update pipeline: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Pipeline{}, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

func encodeJSON(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func decodeFeeds(s string) []string {
	if s == "" {
		return nil
	}
	var feeds []string
	if err := json.Unmarshal([]byte(s), &feeds); err != nil {
		return nil
	}
	return feeds
}

func decodeFilters(s string) Filters {
	if s == "" {
		return Filters{}
	}
	var f Filters
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return Filters{}
	}
	return f
}
