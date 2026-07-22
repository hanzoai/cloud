package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/sites"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// errConflict is returned by CreateProject when (org,slug) already exists;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("projects: project already exists")
	errNotFound = errors.New("projects: project not found")
	// errHostTaken is returned by BindHost when the requested public subdomain is
	// already owned by a DIFFERENT project. Subdomain allocation is first-come and
	// globally unique; a losing bind never blocks a deploy (the site still serves
	// at its S3 URL), it just doesn't claim the pretty host.
	errHostTaken = errors.New("projects: site host already bound to another project")
	// errReservedHost is returned by BindHost for a reserved label (api, admin, a
	// brand/auth term, …). Together with the createProject reject, this makes the
	// global site_hosts table PHYSICALLY UNABLE to hold a reserved host — so a
	// reserved subdomain can never resolve to a project even if the ingress regex
	// drifts. See clients/sites/reserved.go for the ONE reserved-list source.
	errReservedHost = errors.New("projects: site host is a reserved label")
)

// Project is the org-scoped, canonical record of a buildable/deployable site.
// It is the SAME record whether read from hanzo.app (the builder) or
// console.hanzo.ai (the Projects module): org isolation is the org column,
// enforced at the query layer, and the gateway-minted X-Org-Id selects the
// org. Repo fields are flat columns here; the HTTP surface nests them under
// "repo" (see projects.go). It never stores a secret.
//
// Distinct from tracker.Project (not a duplicate): this is the Slug-keyed
// deployable site; a tracker.Project is a KEY-prefixed issue team that lives
// INSIDE one of these (the IAM/deploy project is the tracker's tenant boundary).
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
	// CacheControl is the per-project override for the HTML/document Cache-Control
	// header applied to deployed objects (and honored by the site server). Empty =
	// the honest default (public, max-age=60, s-maxage=86400). Content-hashed
	// assets are always immutable regardless of this override.
	CacheControl string
	// LastPurgeAt is the unix time of the last successful (or attempted) Cloudflare
	// edge purge for this site. Surfaced on the API so a console can show cache freshness.
	LastPurgeAt int64
	CreatedAt   int64
	UpdatedAt   int64
	// Analytics is the per-project web-analytics flag, wired ON by default: a
	// freshly created project collects analytics unless the caller opts out
	// (analytics:false at create). It is the source of truth the app's
	// static-builder reads as deployment.analytics, so the beacon is injected with
	// no opt-in. Mutable via update (read-modify-write); immutable columns are
	// org/slug/id/created_at.
	Analytics bool
	// SpaceId is the project's Base data space — the "<org>/<slug>" namespace under
	// which its deployed site's form/forum/data submissions live in Hanzo Base
	// (/v1/base). Set once at create (the app's namespace/repoId convention);
	// immutable thereafter. A Base space is provisioned best-effort at create.
	SpaceId string
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
// ({DataDir}/projects.db) holds every org's records; org-scoping is the org column.
// MaxOpenConns(1) serializes writes against the file lock without busy retries.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
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
  updated_at      INTEGER NOT NULL,
  analytics       INTEGER NOT NULL DEFAULT 1,
  space_id        TEXT NOT NULL DEFAULT ''
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

-- site_hosts is the GLOBAL public-hostname namespace: it binds a globally-unique
-- host (the subdomain slug for <slug>.hanzo.app; a full custom domain later) to
-- exactly one project (org, slug). Project slugs are only org-unique, so this
-- separate table is what makes a bare subdomain resolve deterministically to one
-- org — the authoritative binding the site server keys on. First-come wins;
-- the PK enforces one owner per host.
CREATE TABLE IF NOT EXISTS site_hosts (
  host        TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  slug        TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_site_hosts_org_slug ON site_hosts(org, slug);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Additive column migrations for existing databases. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so attempt each and ignore the duplicate-column
	// error — the idempotent forward-only migration pattern.
	for _, alter := range []string{
		`ALTER TABLE projects ADD COLUMN cache_control TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN last_purge_at INTEGER NOT NULL DEFAULT 0`,
		// analytics is wired ON by default (DEFAULT 1), so existing projects that
		// predate the column also collect analytics — "wired by default" applies to
		// them too. space_id backfills empty; the deploy/serve paths re-derive it.
		`ALTER TABLE projects ADD COLUMN analytics INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE projects ADD COLUMN space_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(alter); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate alter: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const projectCols = `id,org,slug,name,description,repo_url,repo_branch,repo_provider,framework,status,live_url,bucket,current_deploy,cache_control,last_purge_at,created_at,updated_at,analytics,space_id`

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := sc.Scan(&p.ID, &p.Org, &p.Slug, &p.Name, &p.Description,
		&p.RepoURL, &p.RepoBranch, &p.RepoProvider, &p.Framework,
		&p.Status, &p.LiveURL, &p.Bucket, &p.CurrentDeploy,
		&p.CacheControl, &p.LastPurgeAt, &p.CreatedAt, &p.UpdatedAt,
		&p.Analytics, &p.SpaceId)
	return p, err
}

// CreateProject inserts one project. A UNIQUE(org,slug) violation surfaces as
// errConflict.
func (s *Store) CreateProject(ctx context.Context, p Project) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Slug, p.Name, p.Description,
		p.RepoURL, p.RepoBranch, p.RepoProvider, p.Framework,
		p.Status, p.LiveURL, p.Bucket, p.CurrentDeploy,
		p.CacheControl, p.LastPurgeAt, p.CreatedAt, p.UpdatedAt,
		p.Analytics, p.SpaceId)
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
		`UPDATE projects SET name=?,description=?,repo_url=?,repo_branch=?,repo_provider=?,framework=?,status=?,live_url=?,bucket=?,current_deploy=?,cache_control=?,last_purge_at=?,analytics=?,updated_at=?
		 WHERE org=? AND slug=?`,
		p.Name, p.Description, p.RepoURL, p.RepoBranch, p.RepoProvider, p.Framework,
		p.Status, p.LiveURL, p.Bucket, p.CurrentDeploy, p.CacheControl, p.LastPurgeAt, p.Analytics, p.UpdatedAt, p.Org, p.Slug)
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

// BindHost claims the global public host for (org, slug), first-come. It is
// idempotent for the SAME owner (a re-deploy just refreshes updated_at). If host
// is already bound to a DIFFERENT project it returns errHostTaken WITHOUT
// overwriting — a losing bind must never hijack another org's live subdomain.
func (s *Store) BindHost(ctx context.Context, host, org, slug string, now int64) error {
	// A reserved label may never enter site_hosts, whatever the caller — the
	// storage invariant that makes the serve-time reserved gate a mere backstop.
	if sites.IsReserved(host) {
		return errReservedHost
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var curOrg, curSlug string
	row := tx.QueryRowContext(ctx, `SELECT org, slug FROM site_hosts WHERE host=?`, host)
	switch err := row.Scan(&curOrg, &curSlug); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO site_hosts (host, org, slug, created_at, updated_at) VALUES (?,?,?,?,?)`,
			host, org, slug, now, now); err != nil {
			return fmt.Errorf("bind host: %w", err)
		}
	case err != nil:
		return fmt.Errorf("bind host lookup: %w", err)
	default:
		if curOrg != org || curSlug != slug {
			return errHostTaken
		}
		if _, err := tx.ExecContext(ctx, `UPDATE site_hosts SET updated_at=? WHERE host=?`, now, host); err != nil {
			return fmt.Errorf("touch host: %w", err)
		}
	}
	return tx.Commit()
}

// UnbindHost releases a host binding, but only the row owned by (org, slug) — so
// deleting project A can never drop project B's host even if they somehow shared
// a row (they cannot, but the scoping makes the guarantee explicit).
func (s *Store) UnbindHost(ctx context.Context, host, org, slug string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM site_hosts WHERE host=? AND org=? AND slug=?`, host, org, slug)
	if err != nil {
		return fmt.Errorf("unbind host: %w", err)
	}
	return nil
}

// ResolveHost returns the project a public host is bound to, joining the global
// site_hosts binding to the org-scoped project. This is the authoritative
// slug→project resolution the site server uses; the org and bucket come ONLY from
// here, never from the request. Missing binding OR missing project ⇒ errNotFound.
func (s *Store) ResolveHost(ctx context.Context, host string) (Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+prefixCols("p")+` FROM site_hosts h JOIN projects p ON p.org=h.org AND p.slug=h.slug WHERE h.host=?`, host)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, errNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("resolve host: %w", err)
	}
	return p, nil
}

// ResolveUniqueLiveSlug resolves a BARE subdomain slug (`<slug>.hanzo.app`) to
// the single LIVE project owning that slug across all orgs. Slugs are only
// org-unique, so the bare host is servable ONLY when unambiguous: zero or 2+
// live owners ⇒ errNotFound (each project still serves at its org-scoped host
// and its S3 URL). LIMIT 2 — a second row is the whole ambiguity signal; we
// never enumerate. This is what keeps pre-binding publishes servable with no
// backfill migration.
func (s *Store) ResolveUniqueLiveSlug(ctx context.Context, slug string) (Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE slug=? AND status='live' LIMIT 2`, slug)
	if err != nil {
		return Project{}, fmt.Errorf("resolve slug: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return Project{}, fmt.Errorf("resolve slug: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return Project{}, fmt.Errorf("resolve slug: %w", err)
	}
	if len(out) != 1 {
		return Project{}, errNotFound
	}
	return out[0], nil
}

// ListHostsForProject returns every public host bound to (org, slug), oldest
// first. It powers GET .../domains so a console/user can see which hostnames the
// site serves — its `<slug>.hanzo.app` subdomain plus any bound custom domains.
func (s *Store) ListHostsForProject(ctx context.Context, org, slug string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host FROM site_hosts WHERE org=? AND slug=? ORDER BY created_at ASC, host ASC`, org, slug)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// prefixCols qualifies each projectCols name with a table alias (for JOINs).
func prefixCols(alias string) string {
	parts := strings.Split(projectCols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + c
	}
	return strings.Join(parts, ",")
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
