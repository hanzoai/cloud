package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errConflict is returned when (org,key) or (project,number) already exists;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("tracker: already exists")
	errNotFound = errors.New("tracker: not found")
)

// Project is an org-scoped issue tracker project (a Linear "team"). Its Key is
// the org-unique, uppercase handle that both routes the URL (/v1/tracker/
// projects/:key) AND prefixes every issue identifier (KEY-<number>). Tenant
// isolation is the org column, filtered on every query.
type Project struct {
	ID          string
	Org         string
	Key         string
	Name        string
	Description string
	CreatedAt   int64
	UpdatedAt   int64
}

// Issue is one work item inside a Project. Number is monotonic PER PROJECT
// (KEY-1, KEY-2, …), allocated under the single-writer transaction in
// CreateIssue so it never races. Status is the board column; Labels is a
// comma-joined tag string (split at the HTTP boundary).
type Issue struct {
	ID          string
	ProjectID   string
	Org         string
	Number      int
	Title       string
	Description string
	Status      string
	Priority    string
	Assignee    string
	Labels      string
	CreatedAt   int64
	UpdatedAt   int64
}

// Store is one (org, project)'s tracker database — ONE SQLite file per project
// at {DataDir}/orgs/{orgSlug}/projects/{projectSlug}/tracker.db (opened via
// cloud.TenantDB). tracker is project-scoped: the physical boundary is the IAM
// project, with the org column retained as defense-in-depth. MaxOpenConns(1)
// serializes writes against the file lock (and makes the CreateIssue number
// allocation a safe read-modify-write inside one transaction).
type Store struct {
	db *sql.DB
}

// openStore wraps a tenant DB — already opened + pragma'd by cloud.TenantDB —
// into the tracker store, running its migration. It is the open func the shared
// cloud.TenantStore cache calls once per project file.
func openStore(db *sql.DB) (*Store, error) {
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
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  key          TEXT NOT NULL,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_projects_org_key ON projects(org, key);
CREATE INDEX IF NOT EXISTS ix_projects_org_updated ON projects(org, updated_at);

CREATE TABLE IF NOT EXISTS issues (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  org          TEXT NOT NULL,
  number       INTEGER NOT NULL,
  title        TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'backlog',
  priority     TEXT NOT NULL DEFAULT 'none',
  assignee     TEXT NOT NULL DEFAULT '',
  labels       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_issues_project_number ON issues(project_id, number);
CREATE INDEX IF NOT EXISTS ix_issues_org_project_status ON issues(org, project_id, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const projectCols = `id,org,key,name,description,created_at,updated_at`

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := sc.Scan(&p.ID, &p.Org, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreateProject inserts one project. A UNIQUE(org,key) violation surfaces as
// errConflict.
func (s *Store) CreateProject(ctx context.Context, p Project) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Key, p.Name, p.Description, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

// GetProject returns the project for (org,key) or errNotFound.
func (s *Store) GetProject(ctx context.Context, org, key string) (Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=? AND key=?`, org, key)
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

// UpdateProject overwrites the mutable fields (name, description). org+key+id+
// created_at are immutable.
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name=?,description=?,updated_at=? WHERE org=? AND key=?`,
		p.Name, p.Description, p.UpdatedAt, p.Org, p.Key)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// DeleteProject removes a project and all its issues in one transaction. Reports
// whether a project row was deleted.
func (s *Store) DeleteProject(ctx context.Context, org, key string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE org=? AND key=?`, org, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get for delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE project_id=?`, id); err != nil {
		return false, fmt.Errorf("delete issues: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
		return false, fmt.Errorf("delete project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

const issueCols = `id,project_id,org,number,title,description,status,priority,assignee,labels,created_at,updated_at`

func scanIssue(sc interface{ Scan(...any) error }) (Issue, error) {
	var i Issue
	err := sc.Scan(&i.ID, &i.ProjectID, &i.Org, &i.Number, &i.Title, &i.Description,
		&i.Status, &i.Priority, &i.Assignee, &i.Labels, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

// CreateIssue allocates the next per-project number and inserts the issue in ONE
// transaction. The transaction holds the single pool connection for its whole
// duration, so the MAX(number)+1 read and the INSERT are serialized against any
// concurrent create — the number can never be handed out twice.
func (s *Store) CreateIssue(ctx context.Context, i Issue) (Issue, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number),0)+1 FROM issues WHERE project_id=?`, i.ProjectID).Scan(&next); err != nil {
		return Issue{}, fmt.Errorf("next number: %w", err)
	}
	i.Number = next
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issues (`+issueCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		i.ID, i.ProjectID, i.Org, i.Number, i.Title, i.Description,
		i.Status, i.Priority, i.Assignee, i.Labels, i.CreatedAt, i.UpdatedAt); err != nil {
		return Issue{}, fmt.Errorf("insert issue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, fmt.Errorf("commit: %w", err)
	}
	return i, nil
}

// GetIssue returns one issue scoped to (org, project, number) or errNotFound.
func (s *Store) GetIssue(ctx context.Context, org, projectID string, number int) (Issue, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+issueCols+` FROM issues WHERE org=? AND project_id=? AND number=?`, org, projectID, number)
	i, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, errNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("get issue: %w", err)
	}
	return i, nil
}

// ListIssues returns issues for a project, optionally filtered to one status
// (empty status = all). Ordered by status then number so a status-grouped view
// renders deterministically and a single-column board reads oldest-first.
func (s *Store) ListIssues(ctx context.Context, org, projectID, status string) ([]Issue, error) {
	q := `SELECT ` + issueCols + ` FROM issues WHERE org=? AND project_id=?`
	args := []any{org, projectID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY status ASC, number ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// UpdateIssue overwrites the mutable fields of an issue scoped to (org, project,
// number). id+project_id+org+number+created_at are immutable.
func (s *Store) UpdateIssue(ctx context.Context, i Issue) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE issues SET title=?,description=?,status=?,priority=?,assignee=?,labels=?,updated_at=?
		 WHERE org=? AND project_id=? AND number=?`,
		i.Title, i.Description, i.Status, i.Priority, i.Assignee, i.Labels, i.UpdatedAt,
		i.Org, i.ProjectID, i.Number)
	if err != nil {
		return fmt.Errorf("update issue: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// DeleteIssue removes one issue scoped to (org, project, number). Reports
// whether a row was deleted.
func (s *Store) DeleteIssue(ctx context.Context, org, projectID string, number int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM issues WHERE org=? AND project_id=? AND number=?`, org, projectID, number)
	if err != nil {
		return false, fmt.Errorf("delete issue: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
