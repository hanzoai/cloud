package prompt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Open is the ONE opener: the database is born encrypted under the
	// key cek derives from the process master and the system namespace (or is a
	// schema on hanzo-sql), and comes back with the single-connection cap already
	// applied on SQLite.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags. Importing modernc
	// directly instead would double-register "sqlite" under CGO and panic at
	// init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// errConflict is returned when (org,name) already exists on a strict create;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("prompts: prompt already exists")
	errNotFound = errors.New("prompts: prompt not found")
)

// Prompt is the org-scoped, canonical record of a named, versioned prompt
// template. Tenant isolation is the org column, enforced at the query layer;
// the gateway-minted X-Org-Id (HIP-0026) selects the tenant. A prompt never
// stores a secret — it is template text plus taxonomy.
type Prompt struct {
	ID        string
	Org       string
	Name      string
	Type      string
	Content   string
	Labels    []string
	Tags      []string
	Version   int
	CreatedAt int64
	UpdatedAt int64
}

// Version is the METADATA of one historical revision (its content lives in the
// prompt_versions row but is not returned in bulk — Red MED-1). Creating a
// prompt whose (org,name) already exists appends a new Version and advances the
// prompt's current Version — real, inspectable history, never fabricated.
type Version struct {
	Version   int
	Type      string
	CreatedAt int64
}

// Store is the prompt-library database. ONE database — the system namespace's
// "prompts", a SQLite file or a schema on hanzo-sql — holds every org's records;
// tenancy is the org column. On SQLite, MaxOpenConns(1) serializes writes against
// the file lock. Its SQL is the dialect both engines share (sdk/dialect).
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "prompts", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *Store) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		// Self-heal from the superseded clients/prompt facade. That earlier owner of
		// /v1/prompt (removed when clients/prompts became the ONE owner) created a
		// `prompts` table in the SAME database with a versioned-rows
		// layout that has NO `id` column. Plain CREATE TABLE IF NOT EXISTS no-ops on
		// it, leaving our `SELECT id,... FROM prompts` to fail 500 "no such column:
		// id". The legacy rows hold no durable product data (the facade shipped one
		// release; the console Compute pages never wrote through it), so on that
		// incompatible schema we drop the legacy pair and rebuild forward — no
		// backwards compat, only forwards. Idempotent: once `id` exists it never fires.
		legacy, err := hasLegacyPromptsSchema(ctx, d, tx)
		if err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		if legacy {
			if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS prompt_versions; DROP TABLE IF EXISTS prompts;`); err != nil {
				return fmt.Errorf("drop legacy schema: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, ddl)
		return err
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

const ddl = `
CREATE TABLE IF NOT EXISTS prompts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL DEFAULT 'text',
  content     TEXT NOT NULL DEFAULT '',
  labels      TEXT NOT NULL DEFAULT '[]',
  tags        TEXT NOT NULL DEFAULT '[]',
  version     BIGINT NOT NULL DEFAULT 1,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_prompts_org_name ON prompts(org, name);
CREATE INDEX IF NOT EXISTS ix_prompts_org_updated ON prompts(org, updated_at);

CREATE TABLE IF NOT EXISTS prompt_versions (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  prompt_name TEXT NOT NULL,
  version     BIGINT NOT NULL,
  type        TEXT NOT NULL DEFAULT 'text',
  content     TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pv_org_name_version ON prompt_versions(org, prompt_name, version);
`

// hasLegacyPromptsSchema reports whether a `prompts` table exists but lacks the
// `id` column — the signature of the removed clients/prompt facade's schema.
// A fresh DB (no table) returns false; our own schema (with `id`) returns false.
// Only a SQLite file can hold it: hanzo-sql was made after the facade was removed.
func hasLegacyPromptsSchema(ctx context.Context, d dialect.Dialect, tx *sql.Tx) (bool, error) {
	if d.Postgres() {
		return false, nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='prompts'`).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	has, err := d.HasColumn(ctx, tx, "prompts", "id")
	return !has, err
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func encodeList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

const promptCols = `id,org,name,type,content,labels,tags,version,created_at,updated_at`

func scanPrompt(sc interface{ Scan(...any) error }) (Prompt, error) {
	var p Prompt
	var labels, tags string
	err := sc.Scan(&p.ID, &p.Org, &p.Name, &p.Type, &p.Content, &labels, &tags,
		&p.Version, &p.CreatedAt, &p.UpdatedAt)
	p.Labels = decodeList(labels)
	p.Tags = decodeList(tags)
	return p, err
}

// Upsert creates a prompt at version 1, or — when (org,name) already exists —
// advances it to a new version with the supplied content/type/labels/tags and
// appends the version-history row. The whole operation is one transaction, so a
// prompt's current row and its version log never drift. Returns the resulting
// (post-write) prompt.
func (s *Store) Upsert(ctx context.Context, p Prompt) (Prompt, error) {
	if err := dialect.Text(p.ID, p.Org, p.Name, p.Type, p.Content, encodeList(p.Labels), encodeList(p.Tags)); err != nil {
		return Prompt{}, fmt.Errorf("upsert prompt: %w", err)
	}
	// The current version is read, then the next written: under the store's one
	// write lock, so two writers of one prompt cannot both take the same number.
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Prompt{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var curVersion int
	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT version, created_at FROM prompts WHERE org=$1 AND name=$2`, p.Org, p.Name)
	switch err := row.Scan(&curVersion, &createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		p.Version = 1
		p.CreatedAt = p.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO prompts (`+promptCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			p.ID, p.Org, p.Name, p.Type, p.Content, encodeList(p.Labels), encodeList(p.Tags),
			p.Version, p.CreatedAt, p.UpdatedAt); err != nil {
			return Prompt{}, fmt.Errorf("insert prompt: %w", err)
		}
	case err != nil:
		return Prompt{}, fmt.Errorf("lookup prompt: %w", err)
	default:
		p.Version = curVersion + 1
		p.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE prompts SET type=$1,content=$2,labels=$3,tags=$4,version=$5,updated_at=$6 WHERE org=$7 AND name=$8`,
			p.Type, p.Content, encodeList(p.Labels), encodeList(p.Tags), p.Version, p.UpdatedAt, p.Org, p.Name); err != nil {
			return Prompt{}, fmt.Errorf("update prompt: %w", err)
		}
	}

	vid := p.ID + "-v" + fmt.Sprint(p.Version)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO prompt_versions (id,org,prompt_name,version,type,content,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT DO NOTHING`,
		vid, p.Org, p.Name, p.Version, p.Type, p.Content, p.UpdatedAt)
	if err != nil {
		return Prompt{}, fmt.Errorf("insert version: %w", err)
	}
	// A version already on record under that number (or that id) is the conflict.
	if n, _ := res.RowsAffected(); n == 0 {
		return Prompt{}, errConflict
	}
	if err := tx.Commit(); err != nil {
		return Prompt{}, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

// Get returns the current prompt for (org,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, name string) (Prompt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+promptCols+` FROM prompts WHERE org=$1 AND name=$2`, org, name)
	p, err := scanPrompt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Prompt{}, errNotFound
	}
	if err != nil {
		return Prompt{}, fmt.Errorf("get prompt: %w", err)
	}
	return p, nil
}

// List returns every current prompt for org, most-recently-updated first.
func (s *Store) List(ctx context.Context, org string) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+promptCols+` FROM prompts WHERE org=$1 ORDER BY updated_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Prompt
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan prompt: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Versions returns bounded version-history METADATA for (org,name), newest
// first (content is NOT selected — Red MED-1). The cap means a prompt with an
// unbounded append history still yields a small, constant-size response.
func (s *Store) Versions(ctx context.Context, org, name string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version,type,created_at FROM prompt_versions WHERE org=$1 AND prompt_name=$2 ORDER BY version DESC LIMIT $3`,
		org, name, versionHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.Type, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountVersions returns the TRUE number of versions for (org,name) — the metrics
// count, unaffected by the history-response cap.
func (s *Store) CountVersions(ctx context.Context, org, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM prompt_versions WHERE org=$1 AND prompt_name=$2`, org, name).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count versions: %w", err)
	}
	return n, nil
}

// Delete removes a prompt and its version history. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM prompts WHERE org=$1 AND name=$2`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete prompt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_versions WHERE org=$1 AND prompt_name=$2`, org, name); err != nil {
		return false, fmt.Errorf("delete versions: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}
