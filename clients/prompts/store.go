package prompts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver already in the cloud dep
	// graph (projectsvc/provisioning use it). Blank import registers "sqlite".
	_ "modernc.org/sqlite"
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

// Version is one immutable historical revision of a prompt's content. Creating
// a prompt whose (org,name) already exists appends a new Version and advances
// the prompt's current Version — real, inspectable history, never fabricated.
type Version struct {
	Version   int
	Type      string
	Content   string
	CreatedAt int64
}

// Store is the prompt-library database. ONE SQLite file
// ({DataDir}/prompts.db) holds every org's records; tenancy is the org column.
// MaxOpenConns(1) serializes writes against the file lock.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS prompts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL DEFAULT 'text',
  content     TEXT NOT NULL DEFAULT '',
  labels      TEXT NOT NULL DEFAULT '[]',
  tags        TEXT NOT NULL DEFAULT '[]',
  version     INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_prompts_org_name ON prompts(org, name);
CREATE INDEX IF NOT EXISTS ix_prompts_org_updated ON prompts(org, updated_at);

CREATE TABLE IF NOT EXISTS prompt_versions (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  prompt_name TEXT NOT NULL,
  version     INTEGER NOT NULL,
  type        TEXT NOT NULL DEFAULT 'text',
  content     TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pv_org_name_version ON prompt_versions(org, prompt_name, version);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Prompt{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var curVersion int
	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT version, created_at FROM prompts WHERE org=? AND name=?`, p.Org, p.Name)
	switch err := row.Scan(&curVersion, &createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		p.Version = 1
		p.CreatedAt = p.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO prompts (`+promptCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
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
			`UPDATE prompts SET type=?,content=?,labels=?,tags=?,version=?,updated_at=? WHERE org=? AND name=?`,
			p.Type, p.Content, encodeList(p.Labels), encodeList(p.Tags), p.Version, p.UpdatedAt, p.Org, p.Name); err != nil {
			return Prompt{}, fmt.Errorf("update prompt: %w", err)
		}
	}

	vid := p.ID + "-v" + fmt.Sprint(p.Version)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO prompt_versions (id,org,prompt_name,version,type,content,created_at) VALUES (?,?,?,?,?,?,?)`,
		vid, p.Org, p.Name, p.Version, p.Type, p.Content, p.UpdatedAt); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Prompt{}, errConflict
		}
		return Prompt{}, fmt.Errorf("insert version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Prompt{}, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

// Get returns the current prompt for (org,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, name string) (Prompt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+promptCols+` FROM prompts WHERE org=? AND name=?`, org, name)
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
		`SELECT `+promptCols+` FROM prompts WHERE org=? ORDER BY updated_at DESC, name ASC`, org)
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

// Versions returns the version history for (org,name), newest first.
func (s *Store) Versions(ctx context.Context, org, name string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version,type,content,created_at FROM prompt_versions WHERE org=? AND prompt_name=? ORDER BY version DESC`, org, name)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.Type, &v.Content, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes a prompt and its version history. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM prompts WHERE org=? AND name=?`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete prompt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_versions WHERE org=? AND prompt_name=?`, org, name); err != nil {
		return false, fmt.Errorf("delete versions: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}
