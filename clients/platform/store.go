package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// errConflict is returned when a UNIQUE (org,slug) collides; errNotFound when a
// lookup misses. Handlers map these to HTTP 409 / 404. Tenancy is the `org`
// column on every table, enforced in every WHERE clause — the ONLY isolation
// boundary. A query that forgets `org=?` is a cross-tenant leak, so the store
// exposes NO method that reads a row without the org.
var (
	errConflict = errors.New("platform: already exists")
	errNotFound = errors.New("platform: not found")
)

// Application is a deployable unit under a project (Dokploy: application). It
// deploys as an operator hanzo.ai/v1 Service CR in the tenant-<org> namespace.
// EnvJSON/DomainsJSON hold JSON-encoded []EnvVar / []string; secrets are never
// stored here (secret env is rejected at the boundary until KMS sealing lands).
type Application struct {
	ID            string
	Org           string
	ProjectID     string // the IAM project NAME (owner=org,name) this app lives under
	Slug          string
	Name          string
	Description   string
	Environment   string
	Source        string // git | image
	RepoURL       string
	RepoBranch    string
	RepoProvider  string
	ImageRepo     string
	ImageTag      string
	BuildType     string
	Dockerfile    string
	Port          int
	Replicas      int
	MinScale      int // container-serverless autoscaling floor (0 ⇒ no HPA, fixed Replicas). Set by /v1/run.
	MaxScale      int // container-serverless autoscaling ceiling (0 ⇒ no HPA). Set by /v1/run.
	StorageGB     int // persistent volume size in GiB (0 ⇒ stateless, no volume at all)
	EnvJSON       string
	DomainsJSON   string
	Status        string
	Namespace     string
	CurrentDeploy string
	CreatedAt     int64
	UpdatedAt     int64
}

// Deployment is one immutable build+deploy attempt for an application, versioned
// monotonically per app (Dokploy: deployment).
type Deployment struct {
	ID            string
	Org           string
	ApplicationID string
	Version       int
	Status        string
	Source        string
	Commit        string
	Image         string
	BuildID       string
	Message       string
	CreatedAt     int64
	UpdatedAt     int64
}

// Domain is a BYO custom (arbitrary-host) domain a tenant has claimed for an
// application — `yourco.com` / `app.yourco.com`. The org's own hanzo.app subtree
// hosts and the app's default host are STRUCTURAL (they live in the app's
// DomainsJSON and are validated by suffix), so they are NOT rows here; this table
// exists for the two things a custom host needs that a subtree host does not:
//
//   - GLOBAL UNIQUENESS — Host is the PRIMARY KEY, so exactly one org can ever
//     claim `yourco.com` (the site_hosts model). A second org's claim collides.
//   - an OWNERSHIP-VERIFICATION lifecycle — Status pending → verified, gated on a
//     DNS challenge Token the customer publishes at `_hanzo-challenge.<host>`.
//
// A custom host is rendered into the app's operator ingress (added to DomainsJSON)
// ONLY once its row is `verified` — an unverified claim never reaches the CR.
type Domain struct {
	Host       string
	Org        string
	ProjectID  string
	AppID      string
	AppSlug    string
	Status     string // pending | verified
	Token      string
	CreatedAt  int64
	VerifiedAt int64
}

// Build is one arcd (in-cluster BuildKit) build record (Dokploy fork: build_job).
type Build struct {
	ID            string
	Org           string
	ApplicationID string
	DeploymentID  string
	Status        string
	Image         string
	JobName       string
	LogsRef       string
	CreatedAt     int64
	UpdatedAt     int64
}

// Store is the platform metadata database. ONE SQLite file
// ({DataDir}/platform.db) holds every org's records; tenancy is the org column.
// MaxOpenConns(1) serializes writes against the file lock without busy retries,
// matching projects/provisioning.
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
	// Projects are owned by IAM (see projects.go); platform persists NO project
	// row. platform_apps.project_id holds the IAM project NAME — the (org,name)
	// key an app lives under.
	const ddl = `
CREATE TABLE IF NOT EXISTS platform_apps (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  project_id     TEXT NOT NULL,
  slug           TEXT NOT NULL,
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  environment    TEXT NOT NULL DEFAULT 'production',
  source         TEXT NOT NULL,
  repo_url       TEXT NOT NULL DEFAULT '',
  repo_branch    TEXT NOT NULL DEFAULT '',
  repo_provider  TEXT NOT NULL DEFAULT '',
  image_repo     TEXT NOT NULL DEFAULT '',
  image_tag      TEXT NOT NULL DEFAULT '',
  build_type     TEXT NOT NULL DEFAULT '',
  dockerfile     TEXT NOT NULL DEFAULT '',
  port           INTEGER NOT NULL DEFAULT 8080,
  replicas       INTEGER NOT NULL DEFAULT 1,
  min_scale      INTEGER NOT NULL DEFAULT 0,
  max_scale      INTEGER NOT NULL DEFAULT 0,
  env_json       TEXT NOT NULL DEFAULT '[]',
  domains_json   TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'draft',
  namespace      TEXT NOT NULL DEFAULT '',
  current_deploy TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  compute_metered_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pf_apps_org_project_slug ON platform_apps(org, project_id, slug);
CREATE INDEX IF NOT EXISTS ix_pf_apps_org_project ON platform_apps(org, project_id);

CREATE TABLE IF NOT EXISTS platform_deployments (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  application_id TEXT NOT NULL,
  version        INTEGER NOT NULL,
  status         TEXT NOT NULL,
  source         TEXT NOT NULL DEFAULT 'manual',
  commit_sha     TEXT NOT NULL DEFAULT '',
  image          TEXT NOT NULL DEFAULT '',
  build_id       TEXT NOT NULL DEFAULT '',
  message        TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pf_deploy_app_version ON platform_deployments(application_id, version);
CREATE INDEX IF NOT EXISTS ix_pf_deploy_app_created ON platform_deployments(application_id, created_at);

CREATE TABLE IF NOT EXISTS platform_builds (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  application_id TEXT NOT NULL,
  deployment_id  TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,
  image          TEXT NOT NULL DEFAULT '',
  job_name       TEXT NOT NULL DEFAULT '',
  logs_ref       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_pf_builds_app ON platform_builds(application_id, created_at);

CREATE TABLE IF NOT EXISTS platform_domains (
  host         TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  project_id   TEXT NOT NULL,
  app_id       TEXT NOT NULL,
  app_slug     TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending',
  token        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  verified_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_pf_domains_app ON platform_domains(org, app_id);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Forward-only additive columns for the /v1/run autoscaling bounds. Idempotent:
	// a fresh DB already has them (CREATE TABLE above) so ADD COLUMN reports a
	// duplicate, which is the success case here — never a schema-fork.
	for _, alter := range []string{
		`ALTER TABLE platform_apps ADD COLUMN min_scale INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE platform_apps ADD COLUMN max_scale INTEGER NOT NULL DEFAULT 0`,
		// compute_metered_at is the running-deployment compute meter's watermark:
		// the unix second through which the app's live compute has been billed
		// (computemeter.go). 0 = never metered → first-sight starts the clock with no
		// back-charge. Forward-only, idempotent (duplicate column = already present).
		`ALTER TABLE platform_apps ADD COLUMN compute_metered_at INTEGER NOT NULL DEFAULT 0`,
		// 0 means stateless, which is what every app predating this column is.
		`ALTER TABLE platform_apps ADD COLUMN storage_gb INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(alter); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate scale: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via the caller.
func (s *Store) Close() error { return s.db.Close() }

// DeleteProjectApps removes every application/deployment/build/domain under a
// project (keyed by the IAM project NAME) in ONE transaction, all scoped to org,
// and returns the removed apps so the caller can tear down their operator CRs.
// The project row itself lives in IAM (see projects.go) — deleting it is the
// caller's separate ProjectStore.Delete; this wipes only platform's app tree.
func (s *Store) DeleteProjectApps(ctx context.Context, org, project string) ([]Application, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	apps, err := listAppsTx(ctx, tx, org, project)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_builds WHERE org=? AND application_id=?`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete builds: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_deployments WHERE org=? AND application_id=?`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete deployments: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=? AND app_id=?`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete domains: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_apps WHERE org=? AND project_id=?`, org, project); err != nil {
		return nil, fmt.Errorf("delete apps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return apps, nil
}

// ── applications ─────────────────────────────────────────────────────────────

const appCols = `id,org,project_id,slug,name,description,environment,source,repo_url,repo_branch,repo_provider,image_repo,image_tag,build_type,dockerfile,port,replicas,env_json,domains_json,status,namespace,current_deploy,created_at,updated_at,min_scale,max_scale,storage_gb`

// appScanDest returns the scan destinations for appCols, IN appCols ORDER. It
// exists so a column added to appCols has exactly ONE field list to be added to.
// scanApp and scanRunningApp read the same query prefix; while each spelled the
// fields out separately, adding a column compiled fine and failed at runtime with
// "expected N destination arguments in Scan".
func appScanDest(a *Application) []any {
	return []any{&a.ID, &a.Org, &a.ProjectID, &a.Slug, &a.Name, &a.Description, &a.Environment,
		&a.Source, &a.RepoURL, &a.RepoBranch, &a.RepoProvider, &a.ImageRepo, &a.ImageTag,
		&a.BuildType, &a.Dockerfile, &a.Port, &a.Replicas, &a.EnvJSON, &a.DomainsJSON,
		&a.Status, &a.Namespace, &a.CurrentDeploy, &a.CreatedAt, &a.UpdatedAt, &a.MinScale, &a.MaxScale,
		&a.StorageGB}
}

func scanApp(sc interface{ Scan(...any) error }) (Application, error) {
	var a Application
	err := sc.Scan(appScanDest(&a)...)
	return a, err
}

func (s *Store) CreateApplication(ctx context.Context, a Application) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_apps (`+appCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.ProjectID, a.Slug, a.Name, a.Description, a.Environment,
		a.Source, a.RepoURL, a.RepoBranch, a.RepoProvider, a.ImageRepo, a.ImageTag,
		a.BuildType, a.Dockerfile, a.Port, a.Replicas, a.EnvJSON, a.DomainsJSON,
		a.Status, a.Namespace, a.CurrentDeploy, a.CreatedAt, a.UpdatedAt, a.MinScale, a.MaxScale, a.StorageGB)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert app: %w", err)
	}
	return nil
}

// GetApplication resolves an app by (org, project_id, slug) — the org is ALWAYS
// in the predicate so a caller can never read another tenant's app.
func (s *Store) GetApplication(ctx context.Context, org, projectID, slug string) (Application, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=? AND project_id=? AND slug=?`, org, projectID, slug)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("get app: %w", err)
	}
	return a, nil
}

// GetApplicationByID resolves an app by (org,id) for deployment/build lookups.
func (s *Store) GetApplicationByID(ctx context.Context, org, id string) (Application, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=? AND id=?`, org, id)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("get app by id: %w", err)
	}
	return a, nil
}

func (s *Store) ListApplications(ctx context.Context, org, projectID string) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=? AND project_id=? ORDER BY updated_at DESC, id ASC`, org, projectID)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAllApplications returns every application under org across ALL its
// projects, newest-updated first. It is the org-wide input to the console
// aggregates (environments/pipelines/builds/releases in console.go). Org is the
// ONLY predicate — the SAME tenancy boundary as every other query — so it can
// never surface another tenant's apps.
func (s *Store) ListAllApplications(ctx context.Context, org string) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=? ORDER BY updated_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func listAppsTx(ctx context.Context, tx *sql.Tx, org, projectID string) ([]Application, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=? AND project_id=?`, org, projectID)
	if err != nil {
		return nil, fmt.Errorf("list apps (tx): %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app (tx): %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── compute meter (running-deployment SBOM compute → org spend) ──────────────

// RunningApp is a live application paired with its compute watermark — the input
// to the periodic compute meter (computemeter.go). MeteredAt is the unix second
// through which this app's live compute has already been billed.
type RunningApp struct {
	App       Application
	MeteredAt int64
}

// scanRunningApp scans appCols + compute_metered_at into a RunningApp. It mirrors
// scanApp's field order (the app config) and appends the one meter-state column, so
// the watermark stays orthogonal to the app config every other query reads.
func scanRunningApp(sc interface{ Scan(...any) error }) (RunningApp, error) {
	var ra RunningApp
	err := sc.Scan(append(appScanDest(&ra.App), &ra.MeteredAt)...)
	return ra, err
}

// RunningApps returns every app currently `live` across ALL orgs, paired with its
// compute watermark — the single-writer compute meter's sweep set. It is the
// running-deployment analogue of the build reconciler's ListBuildingDeployments:
// unscoped by org on purpose (the meter runs cluster-wide on the writer pod, then
// meters each app to its OWN org). Only `live` apps run in-cluster and consume
// billable compute — a `stopped`/`building`/`draft` app has no running footprint.
func (s *Store) RunningApps(ctx context.Context) ([]RunningApp, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+appCols+`, compute_metered_at FROM platform_apps WHERE status='live' ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list running apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RunningApp
	for rows.Next() {
		ra, err := scanRunningApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan running app: %w", err)
		}
		out = append(out, ra)
	}
	return out, rows.Err()
}

// AdvanceComputeMeter moves an app's compute watermark from prev to now, but ONLY
// if it still equals prev (compare-and-set). Returns true when THIS call advanced
// it — the caller then, and only then, meters the (now−prev) span. The CAS makes a
// double-tick idempotent: a second tick sees the already-advanced watermark, the
// UPDATE matches 0 rows, and no second debit is emitted. Single-writer already
// serializes ticks; the CAS makes the never-double-charge property hold regardless.
func (s *Store) AdvanceComputeMeter(ctx context.Context, id string, prev, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_apps SET compute_metered_at=? WHERE id=? AND compute_metered_at=?`, now, id, prev)
	if err != nil {
		return false, fmt.Errorf("advance compute meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// StampComputeMeter unconditionally sets an app's compute watermark to now, scoped
// to org. It is called on a resume (stop→start): the meter then charges only the
// new live span, never the stopped gap the app just came back from. FinalizeLive
// stamps the same watermark for the deploy→live path; together they guarantee the
// meter never bills time an app was not `live`.
func (s *Store) StampComputeMeter(ctx context.Context, org, id string, now int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE platform_apps SET compute_metered_at=? WHERE org=? AND id=?`, now, org, id); err != nil {
		return fmt.Errorf("stamp compute meter: %w", err)
	}
	return nil
}

// UpdateApplication overwrites the mutable fields of an app; org+project+slug+id
// are immutable and form the tenancy/identity key.
func (s *Store) UpdateApplication(ctx context.Context, a Application) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_apps SET name=?,description=?,environment=?,source=?,repo_url=?,repo_branch=?,repo_provider=?,image_repo=?,image_tag=?,build_type=?,dockerfile=?,port=?,replicas=?,min_scale=?,max_scale=?,storage_gb=?,env_json=?,domains_json=?,status=?,namespace=?,current_deploy=?,updated_at=?
		 WHERE org=? AND id=?`,
		a.Name, a.Description, a.Environment, a.Source, a.RepoURL, a.RepoBranch, a.RepoProvider,
		a.ImageRepo, a.ImageTag, a.BuildType, a.Dockerfile, a.Port, a.Replicas, a.MinScale, a.MaxScale, a.StorageGB, a.EnvJSON, a.DomainsJSON,
		a.Status, a.Namespace, a.CurrentDeploy, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return fmt.Errorf("update app: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// FinalizeLive advances an application to `live` at deployment d — atomically and
// MONOTONICALLY. It is the ONE way the platform marks an app live, shared by the
// synchronous image deploy (deploy.go) and the async git build reconciler
// (reconcile.go). The app is moved to this deployment ONLY when d's version is at
// least the version of the app's currently-live deployment: the guard is a single
// conditional UPDATE (SQLite serializes it under MaxOpenConns(1)), so an OLDER
// version whose write races in LATE can never overwrite a NEWER one already live
// — no read-then-write TOCTOU. Returns whether the app advanced (false ⇒ a newer
// version is already live, i.e. this deployment was superseded, or the app row is
// gone). Every predicate is org-scoped.
func (s *Store) FinalizeLive(ctx context.Context, d Deployment, imageTag, namespace string, now int64) (bool, error) {
	// compute_metered_at is reset to now: the compute meter charges only THIS live
	// span, never the build/deploy gap the app just crossed to reach live (which the
	// build meter already priced). Every transition INTO live re-stamps the watermark.
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_apps
		    SET status='live', current_deploy=?, image_tag=?, namespace=?, updated_at=?, compute_metered_at=?
		  WHERE org=? AND id=?
		    AND NOT EXISTS (
		          SELECT 1 FROM platform_deployments cur
		           WHERE cur.id = platform_apps.current_deploy
		             AND cur.application_id = platform_apps.id
		             AND cur.version > ?)`,
		d.ID, imageTag, namespace, now, now, d.Org, d.ApplicationID, d.Version)
	if err != nil {
		return false, fmt.Errorf("finalize live: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteApplication removes an app plus its deployments/builds, scoped to org.
func (s *Store) DeleteApplication(ctx context.Context, org, projectID, slug string) (Application, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Application{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT `+appCols+` FROM platform_apps WHERE org=? AND project_id=? AND slug=?`, org, projectID, slug)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, false, nil
	}
	if err != nil {
		return Application{}, false, fmt.Errorf("get for delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_builds WHERE org=? AND application_id=?`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete builds: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_deployments WHERE org=? AND application_id=?`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete deployments: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=? AND app_id=?`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete domains: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_apps WHERE org=? AND id=?`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete app: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Application{}, false, fmt.Errorf("commit: %w", err)
	}
	return a, true, nil
}

// ── deployments ──────────────────────────────────────────────────────────────

const deploymentCols = `id,org,application_id,version,status,source,commit_sha,image,build_id,message,created_at,updated_at`

func scanDeployment(sc interface{ Scan(...any) error }) (Deployment, error) {
	var d Deployment
	err := sc.Scan(&d.ID, &d.Org, &d.ApplicationID, &d.Version, &d.Status, &d.Source,
		&d.Commit, &d.Image, &d.BuildID, &d.Message, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (s *Store) NextVersion(ctx context.Context, appID string) (int, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM platform_deployments WHERE application_id=?`, appID).Scan(&v); err != nil {
		return 0, fmt.Errorf("max version: %w", err)
	}
	return int(v.Int64) + 1, nil
}

func (s *Store) InsertDeployment(ctx context.Context, d Deployment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_deployments (`+deploymentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Org, d.ApplicationID, d.Version, d.Status, d.Source, d.Commit, d.Image, d.BuildID, d.Message, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert deployment: %w", err)
	}
	return nil
}

func (s *Store) UpdateDeployment(ctx context.Context, d Deployment) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_deployments SET status=?,commit_sha=?,image=?,build_id=?,message=?,updated_at=? WHERE org=? AND id=?`,
		d.Status, d.Commit, d.Image, d.BuildID, d.Message, d.UpdatedAt, d.Org, d.ID)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) GetDeployment(ctx context.Context, org, appID, id string) (Deployment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=? AND application_id=? AND id=?`, org, appID, id)
	d, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, errNotFound
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("get deployment: %w", err)
	}
	return d, nil
}

func (s *Store) ListDeployments(ctx context.Context, org, appID string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=? AND application_id=? ORDER BY version DESC`, org, appID)
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

// ListDeploymentsByOrg returns every deployment for org across ALL apps,
// newest-created first. Org-wide input to the console releases/pipelines
// aggregates (console.go); org is the only tenancy predicate.
func (s *Store) ListDeploymentsByOrg(ctx context.Context, org string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=? ORDER BY created_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list deployments by org: %w", err)
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

// ListBuildingDeployments returns every deployment still in the "building" state
// across ALL orgs, oldest first. It is the input to the build reconciler
// (reconcile.go), which owns the git build→deploy handoff. Because the query is
// keyed on status (not org), the reconciler resumes in-flight builds after a
// cloud restart — the goroutine is stateless; the store IS the state. Every
// write the reconciler then makes is still org-scoped (tenant-<row.Org>).
func (s *Store) ListBuildingDeployments(ctx context.Context) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE status='building' ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list building deployments: %w", err)
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

// ── builds ───────────────────────────────────────────────────────────────────

const buildCols = `id,org,application_id,deployment_id,status,image,job_name,logs_ref,created_at,updated_at`

func scanBuild(sc interface{ Scan(...any) error }) (Build, error) {
	var b Build
	err := sc.Scan(&b.ID, &b.Org, &b.ApplicationID, &b.DeploymentID, &b.Status, &b.Image, &b.JobName, &b.LogsRef, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (s *Store) InsertBuild(ctx context.Context, b Build) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_builds (`+buildCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.Org, b.ApplicationID, b.DeploymentID, b.Status, b.Image, b.JobName, b.LogsRef, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert build: %w", err)
	}
	return nil
}

func (s *Store) UpdateBuild(ctx context.Context, b Build) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_builds SET status=?,image=?,job_name=?,logs_ref=?,updated_at=? WHERE org=? AND id=?`,
		b.Status, b.Image, b.JobName, b.LogsRef, b.UpdatedAt, b.Org, b.ID)
	if err != nil {
		return fmt.Errorf("update build: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) GetBuild(ctx context.Context, org, id string) (Build, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+buildCols+` FROM platform_builds WHERE org=? AND id=?`, org, id)
	b, err := scanBuild(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, errNotFound
	}
	if err != nil {
		return Build{}, fmt.Errorf("get build: %w", err)
	}
	return b, nil
}

// ListBuildsByOrg returns every build record for org across ALL apps,
// newest-created first. Org-wide input to the console builds aggregate
// (console.go); org is the only tenancy predicate. These are REAL BuildKit build
// records — the aggregate never fabricates a build that did not run.
func (s *Store) ListBuildsByOrg(ctx context.Context, org string) ([]Build, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+buildCols+` FROM platform_builds WHERE org=? ORDER BY created_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list builds by org: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, fmt.Errorf("scan build: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ── domains (custom BYO hosts) ─────────────────────────────────────────────────

const domainCols = `host,org,project_id,app_id,app_slug,status,token,created_at,verified_at`

func scanDomain(sc interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	err := sc.Scan(&d.Host, &d.Org, &d.ProjectID, &d.AppID, &d.AppSlug, &d.Status, &d.Token, &d.CreatedAt, &d.VerifiedAt)
	return d, err
}

// CreateDomain claims a custom host for an app. Host is the PRIMARY KEY, so a
// second claim of the SAME host (by any org, any app) collides → errConflict.
// This is the global-uniqueness boundary (two orgs can never both claim
// `yourco.com`).
func (s *Store) CreateDomain(ctx context.Context, d Domain) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_domains (`+domainCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		d.Host, d.Org, d.ProjectID, d.AppID, d.AppSlug, d.Status, d.Token, d.CreatedAt, d.VerifiedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert domain: %w", err)
	}
	return nil
}

// LookupDomain resolves a host GLOBALLY (across every org) — the uniqueness
// probe. It is the ONLY store read not scoped to a caller's org, and exists
// solely so the add-domain handler can answer "is this host already claimed, and
// by whom" to decide a 409. The caller MUST NOT echo a foreign row's details
// back to a tenant (it reveals only that the host is taken, never by whom).
func (s *Store) LookupDomain(ctx context.Context, host string) (Domain, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+domainCols+` FROM platform_domains WHERE host=?`, host)
	d, err := scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, false, nil
	}
	if err != nil {
		return Domain{}, false, fmt.Errorf("lookup domain: %w", err)
	}
	return d, true, nil
}

// GetDomain resolves a custom domain scoped to (org, app_id, host) — the tenant
// path used by verify/delete. Org is always in the predicate so a caller can
// never read another tenant's domain row.
func (s *Store) GetDomain(ctx context.Context, org, appID, host string) (Domain, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+domainCols+` FROM platform_domains WHERE org=? AND app_id=? AND host=?`, org, appID, host)
	d, err := scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, errNotFound
	}
	if err != nil {
		return Domain{}, fmt.Errorf("get domain: %w", err)
	}
	return d, nil
}

// ListDomainsByApp returns every custom domain claimed for an app, org-scoped.
func (s *Store) ListDomainsByApp(ctx context.Context, org, appID string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+domainCols+` FROM platform_domains WHERE org=? AND app_id=? ORDER BY created_at ASC, host ASC`, org, appID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDomainVerified flips a pending custom domain to verified, org+app scoped.
// Reports whether a row advanced (false ⇒ no such pending row for this tenant).
func (s *Store) MarkDomainVerified(ctx context.Context, org, appID, host string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_domains SET status='verified', verified_at=? WHERE org=? AND app_id=? AND host=?`,
		now, org, appID, host)
	if err != nil {
		return false, fmt.Errorf("mark domain verified: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteDomain releases a custom domain claim, org+app scoped. Reports whether a
// row was removed.
func (s *Store) DeleteDomain(ctx context.Context, org, appID, host string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=? AND app_id=? AND host=?`, org, appID, host)
	if err != nil {
		return false, fmt.Errorf("delete domain: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
