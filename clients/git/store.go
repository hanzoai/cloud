package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errConflict is returned when (org,project,name) already exists on create;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("git: repo already exists")
	errNotFound = errors.New("git: repo not found")
)

// Repo is the org-scoped, canonical metadata record for one Git repository.
// Org isolation is the (org, project) pair, enforced at the query layer; the
// gateway-minted X-Org-Id (HIP-0026) selects the org and X-Project-Id an
// optional sub-scope. The repo's OBJECTS (packs, refs) live on the billy-backed
// storage under the same (org, project, name) path — this row is only the
// metadata + the last-measured storage size that commerce meters on.
type Repo struct {
	ID            string
	Org           string
	Project       string // may be "" (org-level repo)
	Name          string
	Description   string
	DefaultBranch string
	SizeBytes     int64
	CreatedAt     int64
	UpdatedAt     int64
}

// Store is one org's repo-metadata database — ONE SQLite file per org at
// {DataDir}/orgs/{orgSlug}/git.db (opened via cloud.OrgDB). git is org-scoped,
// not project-scoped: /v1/git/usage is a deliberate org-wide rollup across every
// project, so the physical boundary is the org and the (optional) project is a
// row column. MaxOpenConns(1) serializes writes against the file lock.
type Store struct {
	db *sql.DB
}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB —
// into the repo-metadata store, running its migration. It is the open func the
// shared cloud.OrgStore cache calls once per org file.
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

-- Lifecycle reactors' per-repo config lives in the SAME per-org git.db (no new
-- DB): a repo→Slack-channel subscription and a repo→downstream mirror target.
-- Both key on (org, repo) — git is org-scoped, project is a display column — so a
-- push and a deploy (whose project namespace differs) route through one key.
CREATE TABLE IF NOT EXISTS repo_subscriptions (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  channel    TEXT NOT NULL,
  events     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repo_subs ON repo_subscriptions(org, repo, channel);
CREATE INDEX IF NOT EXISTS ix_repo_subs_repo ON repo_subscriptions(org, repo);

CREATE TABLE IF NOT EXISTS repo_mirrors (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  host       TEXT NOT NULL,
  url        TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repo_mirrors ON repo_mirrors(org, repo, host);
CREATE INDEX IF NOT EXISTS ix_repo_mirrors_repo ON repo_mirrors(org, repo);
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
// already exists in the org.
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

// ── repo→Slack-channel subscriptions (lifecycle notify) ──────────────────────

// Subscription binds a repo (by org+name) to a Slack channel for lifecycle
// notifications. Events is a CSV of LifecycleKind wire names; "" means every
// supported kind. Project is the scope it was created in (display only) — routing
// keys on (org, repo).
type Subscription struct {
	ID        string
	Org       string
	Project   string
	Repo      string
	Channel   string
	Events    string
	CreatedAt int64
}

const subCols = `id,org,project,repo,channel,events,created_at`

func scanSubscription(sc interface{ Scan(...any) error }) (Subscription, error) {
	var v Subscription
	err := sc.Scan(&v.ID, &v.Org, &v.Project, &v.Repo, &v.Channel, &v.Events, &v.CreatedAt)
	return v, err
}

// CreateSubscription inserts a subscription. errConflict when (org,repo,channel)
// already exists — one repo can subscribe a given channel exactly once.
func (s *Store) CreateSubscription(ctx context.Context, v Subscription) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_subscriptions (`+subCols+`) VALUES (?,?,?,?,?,?,?)`,
		v.ID, v.Org, v.Project, v.Repo, v.Channel, v.Events, v.CreatedAt)
	if err != nil {
		if isUnique(err) {
			return errConflict
		}
		return fmt.Errorf("insert subscription: %w", err)
	}
	return nil
}

// ListSubscriptions returns every subscription for (org, repo), newest first.
func (s *Store) ListSubscriptions(ctx context.Context, org, repo string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+subCols+` FROM repo_subscriptions WHERE org=? AND repo=? ORDER BY created_at DESC, id ASC`, org, repo)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Subscription
	for rows.Next() {
		v, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteSubscription removes a subscription by (org, repo, id) — a caller may only
// delete their own org's subscription of the named repo. Reports whether a row went.
func (s *Store) DeleteSubscription(ctx context.Context, org, repo, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM repo_subscriptions WHERE org=? AND repo=? AND id=?`, org, repo, id)
	if err != nil {
		return false, fmt.Errorf("delete subscription: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── repo→downstream mirror targets (outbound mirror) ─────────────────────────

// MirrorTarget is a downstream remote a repo's advanced refs are mirrored to
// (GitHub/GitLab/self). Keyed by (org, repo, host): one target per host per repo.
type MirrorTarget struct {
	ID        string
	Org       string
	Project   string
	Repo      string
	Host      string
	URL       string
	CreatedAt int64
}

const mirrorCols = `id,org,project,repo,host,url,created_at`

func scanMirror(sc interface{ Scan(...any) error }) (MirrorTarget, error) {
	var v MirrorTarget
	err := sc.Scan(&v.ID, &v.Org, &v.Project, &v.Repo, &v.Host, &v.URL, &v.CreatedAt)
	return v, err
}

// CreateMirror inserts a mirror target. errConflict when (org,repo,host) exists.
func (s *Store) CreateMirror(ctx context.Context, v MirrorTarget) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_mirrors (`+mirrorCols+`) VALUES (?,?,?,?,?,?,?)`,
		v.ID, v.Org, v.Project, v.Repo, v.Host, v.URL, v.CreatedAt)
	if err != nil {
		if isUnique(err) {
			return errConflict
		}
		return fmt.Errorf("insert mirror: %w", err)
	}
	return nil
}

// ListMirrors returns every mirror target for (org, repo), newest first.
func (s *Store) ListMirrors(ctx context.Context, org, repo string) ([]MirrorTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mirrorCols+` FROM repo_mirrors WHERE org=? AND repo=? ORDER BY created_at DESC, id ASC`, org, repo)
	if err != nil {
		return nil, fmt.Errorf("list mirrors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MirrorTarget
	for rows.Next() {
		v, err := scanMirror(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mirror: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteMirror removes a mirror target by (org, repo, id). Reports whether a row went.
func (s *Store) DeleteMirror(ctx context.Context, org, repo, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM repo_mirrors WHERE org=? AND repo=? AND id=?`, org, repo, id)
	if err != nil {
		return false, fmt.Errorf("delete mirror: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
