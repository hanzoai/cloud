package projectsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver already in the cloud dep
	// graph (provisioningsvc uses it). Blank import registers the "sqlite" name.
	_ "modernc.org/sqlite"
)

// errConflict is returned by CreateProject when (org,slug) already exists;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("projects: project already exists")
	errNotFound = errors.New("projects: project not found")
)

// Project is the org-scoped, canonical record of a buildable/deployable site.
// It is the SAME record whether read from hanzo.app (the builder) or
// console.hanzo.ai (the Projects module): tenant isolation is the org column,
// enforced at the query layer, and the gateway-minted X-Org-Id selects the
// tenant. Repo fields are flat columns here; the HTTP surface nests them under
// "repo" (see projectsvc.go). It never stores a secret.
type Project struct {
	ID            string
	Org           string
	Slug          string
	Name          string
	Description   string
	RepoURL       string
	RepoBranch    string
	RepoProvider  string
	Framework     string
	Status        string
	LiveURL       string
	Bucket        string
	CurrentDeploy string
	CreatedAt     int64
	UpdatedAt     int64
}

// Deployment is one deploy attempt for a project, versioned monotonically per
// project. A deploy moves through queued→building→uploading→live (or →error);
// the upload path (tar body) lands directly in "live", the git/CI path starts
// "queued" and is flipped by the CI completion call.
type Deployment struct {
	ID        string
	ProjectID string
	Org       string
	Version   int
	Status    string
	Source    string
	Commit    string
	LiveURL   string
	Bucket    string
	Prefix    string
	Files     int
	Bytes     int64
	Message   string
	CreatedAt int64
	UpdatedAt int64
}

// Store is the projects metadata database. ONE SQLite file
// ({DataDir}/projects.db) holds every org's records; tenancy is the org column.
// MaxOpenConns(1) serializes writes against the file lock without busy retries.
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
CREATE TABLE IF NOT EXISTS projects (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  slug            TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  repo_url        TEXT NOT NULL DEFAULT '',
  repo_branch     TEXT NOT NULL DEFAULT '',
  repo_provider   TEXT NOT NULL DEFAULT '',
  framework       TEXT NOT NULL DEFAULT 'static',
  status          TEXT NOT NULL DEFAULT 'draft',
  live_url        TEXT NOT NULL DEFAULT '',
  bucket          TEXT NOT NULL DEFAULT '',
  current_deploy  TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_projects_org_slug ON projects(org, slug);
CREATE INDEX IF NOT EXISTS ix_projects_org_updated ON projects(org, updated_at);

CREATE TABLE IF NOT EXISTS deployments (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL,
  org         TEXT NOT NULL,
  version     INTEGER NOT NULL,
  status      TEXT NOT NULL,
  source      TEXT NOT NULL DEFAULT 'upload',
  commit_sha  TEXT NOT NULL DEFAULT '',
  live_url    TEXT NOT NULL DEFAULT '',
  bucket      TEXT NOT NULL DEFAULT '',
  prefix      TEXT NOT NULL DEFAULT '',
  files       INTEGER NOT NULL DEFAULT 0,
  bytes       INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_deployments_project_version ON deployments(project_id, version);
CREATE INDEX IF NOT EXISTS ix_deployments_project_created ON deployments(project_id, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const projectCols = `id,org,slug,name,description,repo_url,repo_branch,repo_provider,framework,status,live_url,bucket,current_deploy,created_at,updated_at`

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := sc.Scan(&p.ID, &p.Org, &p.Slug, &p.Name, &p.Description,
		&p.RepoURL, &p.RepoBranch, &p.RepoProvider, &p.Framework,
		&p.Status, &p.LiveURL, &p.Bucket, &p.CurrentDeploy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreateProject inserts one project. A UNIQUE(org,slug) violation surfaces as
// errConflict.
func (s *Store) CreateProject(ctx context.Context, p Project) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Slug, p.Name, p.Description,
		p.RepoURL, p.RepoBranch, p.RepoProvider, p.Framework,
		p.Status, p.LiveURL, p.Bucket, p.CurrentDeploy, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

// GetProject returns the project for (org,slug) or errNotFound.
func (s *Store) GetProject(ctx context.Context, org, slug string) (Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=? AND slug=?`, org, slug)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, errNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListProjects returns every project for org, most-recently-updated first.
func (s *Store) ListProjects(ctx context.Context, org string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=? ORDER BY updated_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject overwrites the mutable fields of an existing project. The caller
// reads-modifies-writes the whole Project; org+slug+id+created_at are immutable.
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name=?,description=?,repo_url=?,repo_branch=?,repo_provider=?,framework=?,status=?,live_url=?,bucket=?,current_deploy=?,updated_at=?
		 WHERE org=? AND slug=?`,
		p.Name, p.Description, p.RepoURL, p.RepoBranch, p.RepoProvider, p.Framework,
		p.Status, p.LiveURL, p.Bucket, p.CurrentDeploy, p.UpdatedAt, p.Org, p.Slug)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// DeleteProject removes a project and all its deployment rows. Reports whether a
// project row was deleted.
func (s *Store) DeleteProject(ctx context.Context, org, slug string) (Project, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE org=? AND slug=?`, org, slug)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("get for delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deployments WHERE project_id=?`, p.ID); err != nil {
		return Project{}, false, fmt.Errorf("delete deployments: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, p.ID); err != nil {
		return Project{}, false, fmt.Errorf("delete project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, false, fmt.Errorf("commit: %w", err)
	}
	return p, true, nil
}

const deploymentCols = `id,project_id,org,version,status,source,commit_sha,live_url,bucket,prefix,files,bytes,message,created_at,updated_at`

func scanDeployment(sc interface{ Scan(...any) error }) (Deployment, error) {
	var d Deployment
	err := sc.Scan(&d.ID, &d.ProjectID, &d.Org, &d.Version, &d.Status, &d.Source,
		&d.Commit, &d.LiveURL, &d.Bucket, &d.Prefix, &d.Files, &d.Bytes, &d.Message,
		&d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// NextVersion returns the next monotonic deploy version for a project (1-based).
func (s *Store) NextVersion(ctx context.Context, projectID string) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM deployments WHERE project_id=?`, projectID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("max version: %w", err)
	}
	return int(v.Int64) + 1, nil
}

// InsertDeployment writes one deployment row.
func (s *Store) InsertDeployment(ctx context.Context, d Deployment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployments (`+deploymentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, d.Org, d.Version, d.Status, d.Source, d.Commit, d.LiveURL,
		d.Bucket, d.Prefix, d.Files, d.Bytes, d.Message, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert deployment: %w", err)
	}
	return nil
}

// UpdateDeployment overwrites the mutable fields of a deployment (status flow).
func (s *Store) UpdateDeployment(ctx context.Context, d Deployment) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET status=?,commit_sha=?,live_url=?,bucket=?,prefix=?,files=?,bytes=?,message=?,updated_at=?
		 WHERE id=? AND org=?`,
		d.Status, d.Commit, d.LiveURL, d.Bucket, d.Prefix, d.Files, d.Bytes, d.Message, d.UpdatedAt, d.ID, d.Org)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// GetDeployment returns one deployment scoped to (org, project, id).
func (s *Store) GetDeployment(ctx context.Context, org, projectID, id string) (Deployment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deploymentCols+` FROM deployments WHERE org=? AND project_id=? AND id=?`, org, projectID, id)
	d, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, errNotFound
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("get deployment: %w", err)
	}
	return d, nil
}

// ListDeployments returns deployments for a project, newest version first.
func (s *Store) ListDeployments(ctx context.Context, org, projectID string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deploymentCols+` FROM deployments WHERE org=? AND project_id=? ORDER BY version DESC`, org, projectID)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
