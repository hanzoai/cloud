package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver already in the cloud dep
	// graph (prompts/projectsvc/provisioning use it). Blank import registers
	// the "sqlite" driver name.
	_ "modernc.org/sqlite"
)

// errConflict is returned when (org,project,name) already exists on create;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("git: repo already exists")
	errNotFound = errors.New("git: repo not found")
)

// Repo is the org-scoped, canonical metadata record for one Git repository.
// Tenant isolation is the (org, project) pair, enforced at the query layer; the
// gateway-minted X-Org-Id (HIP-0026) selects the tenant and X-Project-Id an
// optional sub-scope. The repo's OBJECTS (packs, refs) live on the billy-backed
// storage under the same (org, project, name) path — this row is only the
// metadata + the last-measured storage size that commerce meters on.
type Repo struct {
	ID            string
	Org           string
	Project       string // may be "" (org-level repo)
	Name          string
	Description    string
	DefaultBranch string
	SizeBytes     int64
	CreatedAt     int64
	UpdatedAt     int64
}

// Store is the repo-metadata database. ONE SQLite file ({DataDir}/git.db)
// holds every org's repo rows; tenancy is the (org, project) columns.
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
CREATE TABLE IF NOT EXISTS repos (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  project        TEXT NOT NULL DEFAULT '',
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT 'main',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repos_org_project_name ON repos(org, project, name);
CREATE INDEX IF NOT EXISTS ix_repos_org_updated ON repos(org, updated_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const repoCols = `id,org,project,name,description,default_branch,size_bytes,created_at,updated_at`

func scanRepo(sc interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	err := sc.Scan(&r.ID, &r.Org, &r.Project, &r.Name, &r.Description,
		&r.DefaultBranch, &r.SizeBytes, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// Create inserts a new repo row. Returns errConflict when (org,project,name)
// already exists in the tenant.
func (s *Store) Create(ctx context.Context, r Repo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (`+repoCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.Project, r.Name, r.Description, r.DefaultBranch,
		r.SizeBytes, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		if isUnique(err) {
			return errConflict
		}
		return fmt.Errorf("insert repo: %w", err)
	}
	return nil
}

// Get returns the repo for (org,project,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, project, name string) (Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=? AND project=? AND name=?`, org, project, name)
	r, err := scanRepo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, errNotFound
	}
	if err != nil {
		return Repo{}, fmt.Errorf("get repo: %w", err)
	}
	return r, nil
}

// List returns every repo for (org,project), most-recently-updated first.
func (s *Store) List(ctx context.Context, org, project string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=? AND project=? ORDER BY updated_at DESC, name ASC`, org, project)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListOrg returns every repo across ALL projects for org (usage rollup),
// most-recently-updated first.
func (s *Store) ListOrg(ctx context.Context, org string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=? ORDER BY updated_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list org repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetSize records the last-measured storage size for a repo and bumps
// updated_at. Called on create and after each push, so the metered number is
// always the real on-disk size, never a fabricated rollup.
func (s *Store) SetSize(ctx context.Context, org, project, name string, sizeBytes, updatedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE repos SET size_bytes=?, updated_at=? WHERE org=? AND project=? AND name=?`,
		sizeBytes, updatedAt, org, project, name)
	if err != nil {
		return fmt.Errorf("set size: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// Delete removes a repo row. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, project, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM repos WHERE org=? AND project=? AND name=?`, org, project, name)
	if err != nil {
		return false, fmt.Errorf("delete repo: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// isUnique reports whether err is a SQLite UNIQUE-constraint violation (the
// (org,project,name) index), which handlers map to 409 Conflict.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
