package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on hanzo-sql the store is a schema of the system
	// namespace; without it the database is born encrypted under the key cek derives
	// from the process master and that namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags. Importing modernc
	// directly instead would double-register "sqlite" under CGO and panic at
	// init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// errConflict is returned when a UNIQUE (org,slug) collides; errNotFound when a
// lookup misses. Handlers map these to HTTP 409 / 404. Tenancy is the `org`
// column on every table, enforced in every WHERE clause — the ONLY isolation
// boundary. A query that forgets `org=$n` is a cross-tenant leak, so the store
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
	ID           string
	Org          string
	ProjectID    string // the IAM project NAME (owner=org,name) this app lives under
	Slug         string
	Name         string
	Description  string
	Environment  string
	Source       string // git | image
	RepoURL      string
	RepoBranch   string
	RepoProvider string
	ImageRepo    string
	ImageTag     string
	BuildType    string
	Dockerfile   string
	Port         int
	Replicas     int
	MinScale     int // container-serverless autoscaling floor (0 ⇒ no HPA, fixed Replicas). Set by /v1/platform/run.
	MaxScale     int // container-serverless autoscaling ceiling (0 ⇒ no HPA). Set by /v1/platform/run.
	StorageGB    int // persistent volume size in GiB (0 ⇒ stateless, no volume at all)
	// EnvJSON is the app's environment. It is a KMS value (platform_apps.env_json holds
	// its path), so only the reads that serve an app to a caller fill it (GetApplication,
	// GetApplicationByID, ListApplications); the sweeps across every org (AllApplications,
	// RunningApps) and the org-wide aggregates leave it empty. An UpdateApplication of an
	// app whose EnvJSON is empty leaves the environment as it is.
	EnvJSON       string
	DomainsJSON   string
	Status        string
	Namespace     string
	CurrentDeploy string
	CreatedAt     int64
	UpdatedAt     int64

	// envRef is what the row holds in place of the environment: its path in KMS.
	envRef string
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
	Host      string
	Org       string
	ProjectID string
	AppID     string
	AppSlug   string
	Status    string // pending | verified
	// Token is the DNS challenge. It is a KMS value: the row holds its path (tokenRef),
	// and only the reads that serve a domain to its own org (GetDomain, ListDomainsByApp)
	// fill it.
	Token      string
	CreatedAt  int64
	VerifiedAt int64

	tokenRef string
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
	// Platforms are the architectures this build publishes. Empty is the single,
	// default-architecture build. Two or more means the build fanned out to one Job
	// per architecture and its JobName is the index Job that joins them — which is
	// why the list is stored rather than derived: the Jobs carry a TTL and finish
	// far enough apart that the cluster stops being able to answer what was asked
	// for.
	Platforms []string
	// Tags are more tags of Image's repository that the build writes onto the same
	// manifest. Stored for the reason Platforms is: a fan-out writes them from its
	// index Job, which the reconciler launches later from this row.
	Tags []string
	// Digest is the pushed OCI manifest or index digest (`sha256:...`), read from
	// BuildKit's metadata on completion so callers (including CI and agents on
	// private packages) never need a separate GHCR read:packages token to pin it.
	Digest string
	// Reason is why a failed build failed, as the cluster said it: the solve's
	// error line, a container stuck on a Secret, or the deadline. Empty otherwise.
	Reason string
	// Request is what was asked of the door (buildRequest, JSON), kept so a build whose
	// source could not be fetched can be asked again. Empty when it was not kept.
	Request string
	// Attempt is how many times the build has been asked again after a failed fetch.
	Attempt   int
	CreatedAt int64
	UpdatedAt int64
}

// subsystem names the store: the system namespace's "platform". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "platform"

// Two columns hold a credential or can, and neither is kept here. Each is a KMS value
// (internal/escrow), and the column holds its path:
//
//	platform_apps.env_json       a customer application's environment: the values it runs
//	                             with (connection strings, endpoints, keys of a kind the
//	                             secret screen does not name), at
//	                             orgs/<org>/platform/<app id>/env_json@default. An env var
//	                             flagged secret is not in it at all: its value is at
//	                             orgs/<org>/platform/<app slug>/<KEY> (secrets.go). An
//	                             environment of "[]" holds nothing and is kept nowhere.
//	platform_domains.token       the DNS challenge that proves a custom host is the org's, at
//	                             orgs/<org>/platform/<host>/token@default
//
// No WHERE or ORDER BY reads either. Left plain: ids, slugs, the enums, timestamps,
// repo_url (validateRepoURL refuses a URL that embeds a credential, so it names a
// repository and nothing else), image names, domains_json (host names the org published)
// and the build fields.
//
// A file from before holds them in plaintext or sealed as a field (internal/org), bound to
// the row's (org, id) and (org, host). The open writes each to KMS and replaces the column
// with its path; a column that holds a path is left alone, so a second open does nothing.
var (
	appKey    = []string{"org", "id"}
	domainKey = []string{"org", "host"}
)

// Store is the platform metadata database. ONE database — the system namespace's
// "platform", a schema on hanzo-sql or a SQLite file — holds every org's records;
// tenancy is the org column. Its SQL is the dialect both engines share
// (sdk/dialect). On SQLite MaxOpenConns(1) serializes writes against the
// file lock without busy retries, matching projects/provisioning; on hanzo-sql a
// statement that must not interleave is one conditional UPDATE, or a transaction
// that holds the store's write lock (Dialect.BeginWrite).
type Store struct {
	db     *sql.DB
	fields *tenure.Fields // opens a value a file held before KMS; nil on a process with no data-plane master
	esc    *escrow.Escrow // keeps the environments and the challenge tokens
}

// openStore opens the database and moves what it held before KMS into KMS. master is
// cloud.Deps.MasterKey and opens only that old value; k is cloud.Deps.KMS and keeps every
// environment and token. With no KMS an environment is neither kept nor read.
func openStore(dir string, master []byte, k escrow.KMS) (*Store, error) {
	var fields *tenure.Fields
	if c, err := tenure.NewCipher(master); err == nil {
		if fields, err = c.Fields(namespace.System(), subsystem); err != nil {
			return nil, fmt.Errorf("platform: %w", err)
		}
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, fields: fields, esc: escrow.New(k, "platform")}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
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
  port           BIGINT NOT NULL DEFAULT 8080,
  replicas       BIGINT NOT NULL DEFAULT 1,
  min_scale      BIGINT NOT NULL DEFAULT 0,
  max_scale      BIGINT NOT NULL DEFAULT 0,
  env_json       TEXT NOT NULL DEFAULT '[]',
  domains_json   TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'draft',
  namespace      TEXT NOT NULL DEFAULT '',
  current_deploy TEXT NOT NULL DEFAULT '',
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL,
  compute_metered_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pf_apps_org_project_slug ON platform_apps(org, project_id, slug);
CREATE INDEX IF NOT EXISTS ix_pf_apps_org_project ON platform_apps(org, project_id);

CREATE TABLE IF NOT EXISTS platform_deployments (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  application_id TEXT NOT NULL,
  version        BIGINT NOT NULL,
  status         TEXT NOT NULL,
  source         TEXT NOT NULL DEFAULT 'manual',
  commit_sha     TEXT NOT NULL DEFAULT '',
  image          TEXT NOT NULL DEFAULT '',
  build_id       TEXT NOT NULL DEFAULT '',
  message        TEXT NOT NULL DEFAULT '',
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL
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
  platforms      TEXT NOT NULL DEFAULT '',
  tags           TEXT NOT NULL DEFAULT '',
  digest         TEXT NOT NULL DEFAULT '',
  reason         TEXT NOT NULL DEFAULT '',
  request        TEXT NOT NULL DEFAULT '',
  attempt        BIGINT NOT NULL DEFAULT 0,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL
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
  created_at   BIGINT NOT NULL,
  verified_at  BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_pf_domains_app ON platform_domains(org, app_id);

CREATE TABLE IF NOT EXISTS platform_reviews (
  branch     TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  "user"     TEXT NOT NULL,
  app        TEXT NOT NULL,
  tag        TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  closed_at  BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_pf_reviews_member ON platform_reviews(org, "user", created_at);
`
	// The whole migration is one transaction through tx alone (Dialect.Migrate): two
	// replicas opening the store at once do not run its DDL against each other.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		// Forward-only additive columns, each added only when the table lacks it: a
		// fresh database already has most (CREATE TABLE above), and a failed ALTER
		// would abort the whole transaction on PostgreSQL, so none is tried and
		// forgiven.
		for _, add := range []struct{ table, column, def string }{
			{"platform_apps", "min_scale", "BIGINT NOT NULL DEFAULT 0"},
			{"platform_apps", "max_scale", "BIGINT NOT NULL DEFAULT 0"},
			// compute_metered_at is the running-deployment compute meter's watermark:
			// the unix second through which the app's live compute has been billed
			// (computemeter.go). 0 = never metered → first-sight starts the clock with no
			// back-charge.
			{"platform_apps", "compute_metered_at", "BIGINT NOT NULL DEFAULT 0"},
			// 0 means stateless, which is what every app predating this column is.
			{"platform_apps", "storage_gb", "BIGINT NOT NULL DEFAULT 0"},
			// Empty means the single default-architecture build every row predating this
			// column is.
			{"platform_builds", "platforms", "TEXT NOT NULL DEFAULT ''"},
			// Empty means no extra tags, which is what every row predating this column
			// wrote.
			{"platform_builds", "tags", "TEXT NOT NULL DEFAULT ''"},
			// Empty means no recorded manifest digest (builds predating this column or failed).
			{"platform_builds", "digest", "TEXT NOT NULL DEFAULT ''"},
			// Empty means no recorded failure, which is what every row predating this
			// column says.
			{"platform_builds", "reason", "TEXT NOT NULL DEFAULT ''"},
			// Empty means the build's request was not kept, which is what every row
			// predating this column says; such a build is not retried.
			{"platform_builds", "request", "TEXT NOT NULL DEFAULT ''"},
			// How many times a failed source fetch has been tried again; 0 is the first try.
			{"platform_builds", "attempt", "BIGINT NOT NULL DEFAULT 0"},
		} {
			has, err := d.HasColumn(ctx, tx, add.table, add.column)
			if err != nil {
				return fmt.Errorf("migrate column %s.%s: %w", add.table, add.column, err)
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+add.table+` ADD COLUMN `+add.column+` `+add.def); err != nil {
				return fmt.Errorf("migrate column %s.%s: %w", add.table, add.column, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.moveToKMS(ctx)
}

// moveToKMS writes, once, the environments and challenge tokens a file holds from before
// KMS (plain, or sealed as a field) to KMS and replaces each column with its path. It
// runs outside the schema's transaction, because it calls KMS; it compares what it read,
// so another replica that moved the row first leaves nothing to change. An environment of
// "[]" holds nothing and stays as it is.
func (s *Store) moveToKMS(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for _, t := range []struct {
		table, key, col, field string
		kept                   func(string) bool
	}{
		{"platform_apps", "id", "env_json", "env_json", func(v string) bool { return v == "" || v == "[]" }},
		{"platform_domains", "host", "token", "token", func(v string) bool { return v == "" }},
	} {
		rows, err := s.db.QueryContext(ctx, `SELECT org, `+t.key+`, `+t.col+` FROM `+t.table+` WHERE `+t.col+` <> '' AND substr(`+t.col+`, 1, 4) <> 'kms:'`)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		type held struct{ org, key, value string }
		var found []held
		for rows.Next() {
			var h held
			if err := rows.Scan(&h.org, &h.key, &h.value); err != nil {
				_ = rows.Close()
				return fmt.Errorf("migrate: %w", err)
			}
			found = append(found, h)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		for _, h := range found {
			value := h.value
			if strings.HasPrefix(value, "seal:") {
				if s.fields == nil {
					return fmt.Errorf("migrate: %s %s: no data-plane master on this process, so a value the file held before KMS cannot be opened", t.table, h.key)
				}
				r := s.fields.Row(t.table, h.org, h.key)
				if value = r.Open(t.field, value); r.Err() != nil {
					return fmt.Errorf("migrate: %s %s: %w", t.table, h.key, r.Err())
				}
			}
			stored := value
			if !t.kept(value) {
				var err error
				if stored, err = s.esc.Put(ctx, h.org, h.key, t.field, value); err != nil {
					return fmt.Errorf("migrate: %s %s: %w", t.table, h.key, err)
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE `+t.table+` SET `+t.col+` = $1 WHERE org = $2 AND `+t.key+` = $3 AND `+t.col+` = $4`,
				stored, h.org, h.key, h.value); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
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

	apps, err := s.listAppsTx(ctx, tx, org, project)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_builds WHERE org=$1 AND application_id=$2`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete builds: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_deployments WHERE org=$1 AND application_id=$2`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete deployments: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=$1 AND app_id=$2`, org, a.ID); err != nil {
			return nil, fmt.Errorf("delete domains: %w", err)
		}
	}
	var domains []Domain
	for _, a := range apps {
		more, err := s.domainsTx(ctx, tx, org, a.ID)
		if err != nil {
			return nil, err
		}
		domains = append(domains, more...)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_apps WHERE org=$1 AND project_id=$2`, org, project); err != nil {
		return nil, fmt.Errorf("delete apps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return apps, s.forgetAll(ctx, org, apps, domains)
}

// domainsTx is the domains an app has, naming their tokens, inside tx.
func (s *Store) domainsTx(ctx context.Context, tx *sql.Tx, org, appID string) ([]Domain, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+domainCols+` FROM platform_domains WHERE org=$1 AND app_id=$2`, org, appID)
	if err != nil {
		return nil, fmt.Errorf("list domains (tx): %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Domain
	for rows.Next() {
		d, err := s.scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan domain (tx): %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// forgetAll asks KMS to forget the environment of each app and the challenge token of each
// domain, after their rows are gone. A value left is reported, not hidden: the rows no
// longer name it.
func (s *Store) forgetAll(ctx context.Context, org string, apps []Application, domains []Domain) error {
	var errs []error
	for _, a := range apps {
		errs = append(errs, s.esc.Drop(ctx, org, a.envRef))
	}
	for _, d := range domains {
		errs = append(errs, s.esc.Drop(ctx, org, d.tokenRef))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("the rows are gone but KMS still holds a value: %w", err)
	}
	return nil
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
		&a.BuildType, &a.Dockerfile, &a.Port, &a.Replicas, &a.envRef, &a.DomainsJSON,
		&a.Status, &a.Namespace, &a.CurrentDeploy, &a.CreatedAt, &a.UpdatedAt, &a.MinScale, &a.MaxScale,
		&a.StorageGB}
}

// scanApp reads an application. It names the environment and does not read it: a sweep
// across every org asks KMS for nothing. A read that serves an app to a caller then reveals
// it.
func (s *Store) scanApp(sc interface{ Scan(...any) error }) (Application, error) {
	var a Application
	if err := sc.Scan(appScanDest(&a)...); err != nil {
		return a, err
	}
	return a, nil
}

// revealEnv fills in the environment a's row names in KMS. An environment of "[]" is the
// column's own default and is kept nowhere.
func (s *Store) revealEnv(ctx context.Context, a *Application) error {
	switch {
	case a.envRef == "" || a.envRef == "[]":
		a.EnvJSON = "[]"
		return nil
	case !escrow.Held(a.envRef):
		return fmt.Errorf("platform: app %s: the environment is a value where a KMS path belongs", a.ID)
	}
	env, err := s.esc.Get(ctx, a.Org, a.envRef)
	if err != nil {
		return fmt.Errorf("platform: app %s: %w", a.ID, err)
	}
	a.EnvJSON = env
	return nil
}

// keepEnv is what the row holds for env: its path in KMS, or "[]" for an environment that
// holds nothing, which is kept nowhere and forgets the one it replaces (prev, the path the
// row held, if it held one).
func (s *Store) keepEnv(ctx context.Context, org, id, env, prev string) (string, error) {
	if env == "" || env == "[]" {
		if err := s.esc.Drop(ctx, org, prev); err != nil {
			return "", err
		}
		return "[]", nil
	}
	return s.esc.Put(ctx, org, id, "env_json", env)
}

// CreateApplication inserts a; an id or an (org, project, slug) already there is
// errConflict. A conflict is read off the statement (DO NOTHING inserts no row)
// rather than off an engine's error text, which each engine words alike only to
// itself.
func (s *Store) CreateApplication(ctx context.Context, a Application) error {
	env, err := s.keepEnv(ctx, a.Org, a.ID, a.EnvJSON, "")
	if err != nil {
		return fmt.Errorf("insert app: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_apps (`+appCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		 ON CONFLICT DO NOTHING`,
		a.ID, a.Org, a.ProjectID, a.Slug, a.Name, a.Description, a.Environment,
		a.Source, a.RepoURL, a.RepoBranch, a.RepoProvider, a.ImageRepo, a.ImageTag,
		a.BuildType, a.Dockerfile, a.Port, a.Replicas, env, a.DomainsJSON,
		a.Status, a.Namespace, a.CurrentDeploy, a.CreatedAt, a.UpdatedAt, a.MinScale, a.MaxScale, a.StorageGB)
	if err != nil {
		_ = s.esc.Drop(ctx, a.Org, env)
		return fmt.Errorf("insert app: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// A slug already taken leaves the environment kept for an id no row has; forget
		// it. An id that a row has is that app's, and its environment is not ours to forget.
		var rows int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_apps WHERE org=$1 AND id=$2`, a.Org, a.ID).Scan(&rows); err == nil && rows == 0 {
			_ = s.esc.Drop(ctx, a.Org, env)
		}
		return errConflict
	}
	return nil
}

// GetApplication resolves an app by (org, project_id, slug) — the org is ALWAYS
// in the predicate so a caller can never read another tenant's app.
func (s *Store) GetApplication(ctx context.Context, org, projectID, slug string) (Application, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=$1 AND project_id=$2 AND slug=$3`, org, projectID, slug)
	a, err := s.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("get app: %w", err)
	}
	if err := s.revealEnv(ctx, &a); err != nil {
		return Application{}, err
	}
	return a, nil
}

// GetApplicationByID resolves an app by (org,id) for deployment/build lookups.
func (s *Store) GetApplicationByID(ctx context.Context, org, id string) (Application, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=$1 AND id=$2`, org, id)
	a, err := s.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("get app by id: %w", err)
	}
	if err := s.revealEnv(ctx, &a); err != nil {
		return Application{}, err
	}
	return a, nil
}

func (s *Store) ListApplications(ctx context.Context, org, projectID string) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=$1 AND project_id=$2 ORDER BY updated_at DESC, id ASC`, org, projectID)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := s.scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Revealed once the cursor is closed: a file's one connection serves one query at a
	// time, and the reveal may wait on KMS.
	_ = rows.Close()
	for i := range out {
		if err := s.revealEnv(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListAllApplications returns every application under org across ALL its
// projects, newest-updated first. It is the org-wide input to the console
// aggregates (environments/pipelines/builds/releases in console.go). Org is the
// ONLY predicate — the SAME tenancy boundary as every other query — so it can
// never surface another tenant's apps.
func (s *Store) ListAllApplications(ctx context.Context, org string) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=$1 ORDER BY updated_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := s.scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) listAppsTx(ctx context.Context, tx *sql.Tx, org, projectID string) ([]Application, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+appCols+` FROM platform_apps WHERE org=$1 AND project_id=$2`, org, projectID)
	if err != nil {
		return nil, fmt.Errorf("list apps (tx): %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := s.scanApp(rows)
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
func (s *Store) scanRunningApp(sc interface{ Scan(...any) error }) (RunningApp, error) {
	var ra RunningApp
	if err := sc.Scan(append(appScanDest(&ra.App), &ra.MeteredAt)...); err != nil {
		return ra, err
	}
	return ra, nil
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
		ra, err := s.scanRunningApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan running app: %w", err)
		}
		out = append(out, ra)
	}
	return out, rows.Err()
}

// AllApplications returns every app across ALL orgs — the orphan reaper's sweep
// set (orphans.go). Unscoped by org for the same reason RunningApps is: the
// reaper runs cluster-wide, then acts on each row against that row's OWN org.
// Every other read in this file carries `org=?`; this one is deliberate and is
// used only by a cluster-wide sweep that never serves a request.
func (s *Store) AllApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appCols+` FROM platform_apps ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Application
	for rows.Next() {
		a, err := s.scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		out = append(out, a)
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
		`UPDATE platform_apps SET compute_metered_at=$1 WHERE id=$2 AND compute_metered_at=$3`, now, id, prev)
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
		`UPDATE platform_apps SET compute_metered_at=$1 WHERE org=$2 AND id=$3`, now, org, id); err != nil {
		return fmt.Errorf("stamp compute meter: %w", err)
	}
	return nil
}

// UpdateApplication overwrites the mutable fields of an app; org+project+slug+id
// are immutable and form the tenancy/identity key.
func (s *Store) UpdateApplication(ctx context.Context, a Application) error {
	env := ""
	if a.EnvJSON != "" { // an app read without its environment leaves it as it is
		var err error
		if env, err = s.keepEnv(ctx, a.Org, a.ID, a.EnvJSON, a.envRef); err != nil {
			return fmt.Errorf("update app: %w", err)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_apps SET name=$1,description=$2,environment=$3,source=$4,repo_url=$5,repo_branch=$6,repo_provider=$7,image_repo=$8,image_tag=$9,build_type=$10,dockerfile=$11,port=$12,replicas=$13,min_scale=$14,max_scale=$15,storage_gb=$16,env_json=CASE WHEN $17 = '' THEN env_json ELSE $17 END,domains_json=$18,status=$19,namespace=$20,current_deploy=$21,updated_at=$22
		 WHERE org=$23 AND id=$24`,
		a.Name, a.Description, a.Environment, a.Source, a.RepoURL, a.RepoBranch, a.RepoProvider,
		a.ImageRepo, a.ImageTag, a.BuildType, a.Dockerfile, a.Port, a.Replicas, a.MinScale, a.MaxScale, a.StorageGB, env, a.DomainsJSON,
		a.Status, a.Namespace, a.CurrentDeploy, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return fmt.Errorf("update app: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = s.esc.Drop(ctx, a.Org, env)
		return errNotFound
	}
	return nil
}

// FinalizeLive advances an application to `live` at deployment d — atomically and
// MONOTONICALLY. It is the ONE way the platform marks an app live, shared by the
// synchronous image deploy (deploy.go) and the async git build reconciler
// (reconcile.go). The app is moved to this deployment ONLY when d's version is at
// least the version of the app's currently-live deployment: the guard is a single
// conditional UPDATE (one statement, one row lock on either engine), so an OLDER
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
		    SET status='live', current_deploy=$1, image_tag=$2, namespace=$3, updated_at=$4, compute_metered_at=$5
		  WHERE org=$6 AND id=$7
		    AND NOT EXISTS (
		          SELECT 1 FROM platform_deployments cur
		           WHERE cur.id = platform_apps.current_deploy
		             AND cur.application_id = platform_apps.id
		             AND cur.version > $8)`,
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

	row := tx.QueryRowContext(ctx, `SELECT `+appCols+` FROM platform_apps WHERE org=$1 AND project_id=$2 AND slug=$3`, org, projectID, slug)
	a, err := s.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, false, nil
	}
	if err != nil {
		return Application{}, false, fmt.Errorf("get for delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_builds WHERE org=$1 AND application_id=$2`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete builds: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_deployments WHERE org=$1 AND application_id=$2`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete deployments: %w", err)
	}
	domains, err := s.domainsTx(ctx, tx, org, a.ID)
	if err != nil {
		return Application{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=$1 AND app_id=$2`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete domains: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_apps WHERE org=$1 AND id=$2`, org, a.ID); err != nil {
		return Application{}, false, fmt.Errorf("delete app: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Application{}, false, fmt.Errorf("commit: %w", err)
	}
	// The caller tears the app down from what it returns, environment included.
	if err := s.revealEnv(ctx, &a); err != nil {
		return a, true, err
	}
	return a, true, s.forgetAll(ctx, org, []Application{a}, domains)
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
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM platform_deployments WHERE application_id=$1`, appID).Scan(&v); err != nil {
		return 0, fmt.Errorf("max version: %w", err)
	}
	return int(v.Int64) + 1, nil
}

// InsertDeployment inserts d; an id, or a version the application already has, is
// errConflict.
func (s *Store) InsertDeployment(ctx context.Context, d Deployment) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_deployments (`+deploymentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT DO NOTHING`,
		d.ID, d.Org, d.ApplicationID, d.Version, d.Status, d.Source, d.Commit, d.Image, d.BuildID, d.Message, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert deployment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errConflict
	}
	return nil
}

func (s *Store) UpdateDeployment(ctx context.Context, d Deployment) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_deployments SET status=$1,commit_sha=$2,image=$3,build_id=$4,message=$5,updated_at=$6 WHERE org=$7 AND id=$8`,
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
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=$1 AND application_id=$2 AND id=$3`, org, appID, id)
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
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=$1 AND application_id=$2 ORDER BY version DESC`, org, appID)
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
		`SELECT `+deploymentCols+` FROM platform_deployments WHERE org=$1 ORDER BY created_at DESC, id ASC`, org)
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

// ListLiveDeploying lists the deployments an app is serving (its current_deploy) that
// still read "deploying": applied to the cluster, not yet seen healthy. A deployment
// its app has moved past is not here, so the list stays as long as the apps rolling out
// now rather than as every deployment ever made.
func (s *Store) ListLiveDeploying(ctx context.Context) ([]Deployment, error) {
	cols := "d." + strings.ReplaceAll(deploymentCols, ",", ",d.")
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM platform_deployments d
		   JOIN platform_apps a ON a.id = d.application_id AND a.org = d.org AND a.current_deploy = d.id
		  WHERE d.status='deploying' ORDER BY d.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list deploying deployments: %w", err)
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

const buildCols = `id,org,application_id,deployment_id,status,image,job_name,logs_ref,platforms,tags,digest,reason,request,attempt,created_at,updated_at`

func scanBuild(sc interface{ Scan(...any) error }) (Build, error) {
	var b Build
	var platforms, tags string
	err := sc.Scan(&b.ID, &b.Org, &b.ApplicationID, &b.DeploymentID, &b.Status, &b.Image, &b.JobName, &b.LogsRef, &platforms, &tags, &b.Digest, &b.Reason, &b.Request, &b.Attempt, &b.CreatedAt, &b.UpdatedAt)
	b.Platforms = strings.Fields(platforms)
	b.Tags = strings.Fields(tags)
	return b, err
}

func (s *Store) InsertBuild(ctx context.Context, b Build) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_builds (`+buildCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		b.ID, b.Org, b.ApplicationID, b.DeploymentID, b.Status, b.Image, b.JobName, b.LogsRef, strings.Join(b.Platforms, " "), strings.Join(b.Tags, " "), b.Digest, b.Reason, b.Request, b.Attempt, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert build: %w", err)
	}
	return nil
}

func (s *Store) UpdateBuild(ctx context.Context, b Build) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_builds SET status=$1,image=$2,job_name=$3,logs_ref=$4,digest=$5,reason=$6,attempt=$7,updated_at=$8 WHERE org=$9 AND id=$10`,
		b.Status, b.Image, b.JobName, b.LogsRef, b.Digest, b.Reason, b.Attempt, b.UpdatedAt, b.Org, b.ID)
	if err != nil {
		return fmt.Errorf("update build: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) GetBuild(ctx context.Context, org, id string) (Build, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+buildCols+` FROM platform_builds WHERE org=$1 AND id=$2`, org, id)
	b, err := scanBuild(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, errNotFound
	}
	if err != nil {
		return Build{}, fmt.Errorf("get build: %w", err)
	}
	return b, nil
}

// ListUnfinishedBuilds returns every build still in a non-terminal state that
// carries a Job name, oldest first, across ALL orgs.
//
// It exists because a build made through POST /v1/platform/runner has NO deployment. The
// reconciler drove itself off ListBuildingDeployments alone, and reconcileBuild
// returns early for anything whose Source is not "git" — so a direct build's row
// was written "queued" and NOTHING ever advanced it. Its status stayed "queued"
// for the life of the row, whether the Job had succeeded, failed, or never been
// scheduled at all.
//
// That is not a cosmetic gap. It made GET /v1/platform/builds unable to answer the only
// question it is asked: a build that pushed an image and a build that could not
// be scheduled read IDENTICALLY. Six builds sat unschedulable for five days and
// looked exactly like six that were merely in flight, which is precisely why
// nobody could see it.
//
// Keyed on status, not org, for the same reason as ListBuildingDeployments: the
// reconciler is stateless and resumes after a restart because the store IS the
// state. Rows with no job_name are skipped — there is no Job to ask about, and
// the deadline sweep in the deployment path owns that case.
func (s *Store) ListUnfinishedBuilds(ctx context.Context) ([]Build, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+buildCols+` FROM platform_builds
		  WHERE status NOT IN ('succeeded','failed') AND job_name<>''
		  ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list unfinished builds: %w", err)
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

// ListBuildsByOrg returns every build record for org across ALL apps,
// newest-created first. Org-wide input to the console builds aggregate
// (console.go); org is the only tenancy predicate. These are REAL BuildKit build
// records — the aggregate never fabricates a build that did not run.
func (s *Store) ListBuildsByOrg(ctx context.Context, org string) ([]Build, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+buildCols+` FROM platform_builds WHERE org=$1 ORDER BY created_at DESC, id ASC`, org)
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

// scanDomain reads a domain. It names the challenge token and does not read it: the global
// uniqueness probe (LookupDomain) answers for hosts of other orgs and must not.
func (s *Store) scanDomain(sc interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	err := sc.Scan(&d.Host, &d.Org, &d.ProjectID, &d.AppID, &d.AppSlug, &d.Status, &d.tokenRef, &d.CreatedAt, &d.VerifiedAt)
	return d, err
}

// revealToken fills in the challenge token d's row names in KMS.
func (s *Store) revealToken(ctx context.Context, d *Domain) error {
	token, err := s.esc.Get(ctx, d.Org, d.tokenRef)
	if err != nil {
		return fmt.Errorf("platform: domain %s: %w", d.Host, err)
	}
	d.Token = token
	return nil
}

// CreateDomain claims a custom host for an app. Host is the PRIMARY KEY, so a
// second claim of the SAME host (by any org, any app) collides → errConflict.
// This is the global-uniqueness boundary (two orgs can never both claim
// `yourco.com`).
func (s *Store) CreateDomain(ctx context.Context, d Domain) error {
	// The host is the claim. Another org's claim of it is checked before anything is
	// kept, because what is kept is under the claiming org's path and a conflict would
	// leave it there.
	var taken int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_domains WHERE host=$1`, d.Host).Scan(&taken); err != nil {
		return fmt.Errorf("insert domain: %w", err)
	}
	if taken > 0 {
		return errConflict
	}
	token, err := s.esc.Put(ctx, d.Org, d.Host, "token", d.Token)
	if err != nil {
		return fmt.Errorf("insert domain: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO platform_domains (`+domainCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT DO NOTHING`,
		d.Host, d.Org, d.ProjectID, d.AppID, d.AppSlug, d.Status, token, d.CreatedAt, d.VerifiedAt)
	if err != nil {
		_ = s.esc.Drop(ctx, d.Org, token)
		return fmt.Errorf("insert domain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Two claims of one host raced past the check; the one that lost keeps nothing,
		// and the winner's token is at a path of the winner's org or this one's.
		var mine int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_domains WHERE host=$1 AND org=$2`, d.Host, d.Org).Scan(&mine); err == nil && mine == 0 {
			_ = s.esc.Drop(ctx, d.Org, token)
		}
		return errConflict
	}
	return nil
}

// LookupDomain resolves a host GLOBALLY (across every org) — the uniqueness
// probe. It is the ONLY store read not scoped to a caller's org, and exists
// solely so the add-domain handler can answer "is this host already claimed, and
// by whom" to decide a 409. The caller MUST NOT echo a foreign row's details
// back to a tenant (it reveals only that the host is taken, never by whom).
func (s *Store) LookupDomain(ctx context.Context, host string) (Domain, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+domainCols+` FROM platform_domains WHERE host=$1`, host)
	d, err := s.scanDomain(row)
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
		`SELECT `+domainCols+` FROM platform_domains WHERE org=$1 AND app_id=$2 AND host=$3`, org, appID, host)
	d, err := s.scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, errNotFound
	}
	if err != nil {
		return Domain{}, fmt.Errorf("get domain: %w", err)
	}
	if err := s.revealToken(ctx, &d); err != nil {
		return Domain{}, err
	}
	return d, nil
}

// ListDomainsByApp returns every custom domain claimed for an app, org-scoped.
func (s *Store) ListDomainsByApp(ctx context.Context, org, appID string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+domainCols+` FROM platform_domains WHERE org=$1 AND app_id=$2 ORDER BY created_at ASC, host ASC`, org, appID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Domain
	for rows.Next() {
		d, err := s.scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for i := range out {
		if err := s.revealToken(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MarkDomainVerified flips a pending custom domain to verified, org+app scoped.
// Reports whether a row advanced (false ⇒ no such pending row for this tenant).
func (s *Store) MarkDomainVerified(ctx context.Context, org, appID, host string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE platform_domains SET status='verified', verified_at=$1 WHERE org=$2 AND app_id=$3 AND host=$4`,
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
	d, err := s.GetDomain(ctx, org, appID, host)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM platform_domains WHERE org=$1 AND app_id=$2 AND host=$3`, org, appID, host)
	if err != nil {
		return false, fmt.Errorf("delete domain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := s.esc.Drop(ctx, org, d.tokenRef); err != nil {
		return true, fmt.Errorf("delete domain: the claim is gone but KMS still holds its token: %w", err)
	}
	return true, nil
}
