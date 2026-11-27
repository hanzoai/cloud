package world

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// The world PIPELINE store is the durable, per-(org,project) config for the news
// data plane: the RSS feed URLs to poll plus the region/keyword/source filters
// applied to the merged result. It is the metastore twin of the settings store
// (apps/settings/store.go) and follows the same discipline — one database for every
// org, a SQLite file or a schema on hanzo-sql, in the dialect both engines share
// (sdk/dialect), and its tests run on both (internal/sqltest).
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
	// Regions keeps only items whose TITLE contains one of these, matched
	// case-insensitively as a substring. Empty keeps every region.
	Regions []string `json:"regions"`
	// Keywords keeps only items whose TITLE contains one of these,
	// case-insensitively. They are also the GDELT queries the feed fans out to,
	// one per keyword of three characters or more — so a keyword both widens what
	// is fetched and narrows what is kept.
	Keywords []string `json:"keywords"`
	// Sources keeps only items whose outlet name contains one of these,
	// case-insensitively. Empty keeps every outlet.
	Sources []string `json:"sources"`
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

// PipelineStore is the world pipeline metastore over one database — the
// deployment's own "world" subsystem. Tenancy is the (org, project) key.
type PipelineStore struct {
	db *sql.DB
	d  dialect.Dialect
}

func openPipelineStore(dir string) (*PipelineStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "world", dir, func(db *sql.DB) (*PipelineStore, error) {
		s := &PipelineStore{db: db, d: dialect.Of(db)}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *PipelineStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS pipelines (
  org        TEXT NOT NULL,
  project    TEXT NOT NULL,
  feeds      TEXT NOT NULL DEFAULT '[]',
  filters    TEXT NOT NULL DEFAULT '{}',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (org, project)
);
CREATE INDEX IF NOT EXISTS ix_pipelines_org ON pipelines(org, updated_at);`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}); err != nil {
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
		   FROM pipelines WHERE org=$1 AND project=$2`, org, project)
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
	if err := dialect.Text(p.Org, p.Project, feedsJSON, filtersJSON); err != nil {
		return Pipeline{}, fmt.Errorf("pipeline: %w", err)
	}

	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Pipeline{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM pipelines WHERE org=$1 AND project=$2`, p.Org, p.Project)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		p.CreatedAt = p.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pipelines (org, project, feeds, filters, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			p.Org, p.Project, feedsJSON, filtersJSON, p.CreatedAt, p.UpdatedAt); err != nil {
			return Pipeline{}, fmt.Errorf("insert pipeline: %w", err)
		}
	case err != nil:
		return Pipeline{}, fmt.Errorf("lookup pipeline: %w", err)
	default:
		p.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE pipelines SET feeds=$1, filters=$2, updated_at=$3 WHERE org=$4 AND project=$5`,
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
