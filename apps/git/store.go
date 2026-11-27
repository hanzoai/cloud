package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
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
	Public        bool // public repos allow ANONYMOUS read (upload-pack); writes stay org-authed
	SizeBytes     int64
	// Origin is the LINK: the clone address of the host this repo's history
	// lives on (link.go), recorded by a link the host let its caller read. A
	// linked repo's directory here is a cache — reads fill it, writes go to the
	// host, and releasing it costs a fetch (reclaim.go). Empty means the history
	// is here, and the directory is the only copy there is.
	//
	// It lives on the ROW and not in the repo's git config because eviction
	// deletes the directory the config is in. A fact that dies with the thing it
	// describes cannot be the reason it was safe to delete it.
	Origin    string
	CreatedAt int64
	UpdatedAt int64
}

// Store is one org's repo-metadata database — ONE database per org: a SQLite file
// at {DataDir}/orgs/{orgSlug}/git.db (opened via cloud.OrgDB), or on hanzo-sql a
// schema of that name. git is org-scoped, not project-scoped: /v1/git/usage is a
// deliberate org-wide rollup across every project, so the physical boundary is the
// org and the (optional) project is a row column. It holds the repos' rows, their
// subscriptions, mirror targets and pull requests, and the CI pools, runners, runs,
// tasks and push journal; a repository's OBJECTS are not here but in the billy-backed
// storage. On a file MaxOpenConns(1) serializes writes against the file lock; on
// hanzo-sql a transaction that reads then writes starts with BeginWrite. Its SQL is
// the dialect both engines share (sdk/dialect).
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

// migrate brings the database to the current schema in ONE transaction that holds the
// store's migration lock; everything below goes through tx.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error { return migrateIn(ctx, d, tx) }); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func migrateIn(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS repos (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  project        TEXT NOT NULL DEFAULT '',
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT 'main',
  public         BIGINT NOT NULL DEFAULT 0,
  size_bytes     BIGINT NOT NULL DEFAULT 0,
  origin         TEXT NOT NULL DEFAULT '',
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repos_org_project_name ON repos(org, project, name);
CREATE INDEX IF NOT EXISTS ix_repos_org_updated ON repos(org, updated_at);

-- Lifecycle reactors' per-repo config lives in the SAME per-org git.db (no new
-- DB): a repo→Slack-channel subscription and a repo→downstream mirror target.
-- Both key on the FULL repo identity (org, project, repo) — the same tuple the
-- repos table + storage path use — so a repo of the same name under project A and
-- project B are distinct: a push to B can never mirror-out B's code to A's
-- downstream or notify A's channel. Rows cascade-delete with their repo.
CREATE TABLE IF NOT EXISTS repo_subscriptions (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  channel    TEXT NOT NULL,
  events     TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repo_subs ON repo_subscriptions(org, project, repo, channel);
CREATE INDEX IF NOT EXISTS ix_repo_subs_repo ON repo_subscriptions(org, project, repo);

CREATE TABLE IF NOT EXISTS repo_mirrors (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  host       TEXT NOT NULL,
  url        TEXT NOT NULL,
  created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repo_mirrors ON repo_mirrors(org, project, repo, host);
CREATE INDEX IF NOT EXISTS ix_repo_mirrors_repo ON repo_mirrors(org, project, repo);

-- pulls is the proposal to move one branch into another, and the ONLY row in this
-- schema that outlives the two refs it names: a branch is deleted by forgetting
-- it, and the record of what was proposed and what happened to it must not be.
-- number is per-repo and dense from 1 — the handle a person and an agent both use
-- ("#4"), allocated under the same (org, project, repo) tuple every other table
-- keys on, so #4 of project A and #4 of project B are different rows.
--
-- The partial unique index is the structural half of "one open proposal per
-- (base, head)": CreatePull also checks inside its transaction, and this is what
-- holds if that check ever races. Merged and closed rows are outside the index,
-- so the same branch may be proposed again after its first proposal resolves.
CREATE TABLE IF NOT EXISTS pulls (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  number     BIGINT NOT NULL,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  base       TEXT NOT NULL,
  head       TEXT NOT NULL,
  state      TEXT NOT NULL DEFAULT 'open',
  author     TEXT NOT NULL DEFAULT '',
  merged_rev TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pulls_number ON pulls(org, project, repo, number);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pulls_open ON pulls(org, project, repo, base, head) WHERE state='open';
CREATE INDEX IF NOT EXISTS ix_pulls_repo ON pulls(org, project, repo, state);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	// The CI half of the same database: the pools an org declares, the runs a commit
	// opens, the tasks a runner leases (run.go), and the journal a push is
	// recorded in before it is answered (journal.go). One org, one database —
	// a run belongs to a repository and a repository is already stored here.
	if _, err := tx.ExecContext(ctx, runDDL(d)+journalDDL+statusDDL); err != nil {
		return fmt.Errorf("runs: %w", err)
	}
	// public: visibility bit added after the initial schema. Fresh DBs get it
	// from the CREATE TABLE above; pre-existing DBs gain it here.
	// origin: added with the bound in reclaim.go. It defaults EMPTY, so every
	// repo that predates this column is pinned until somebody says where it came
	// from — which is the safe direction, and the only one available: the bytes
	// on disk carry no remote, so the migration cannot infer an answer.
	for _, c := range []struct{ name, decl string }{
		{"public", "BIGINT NOT NULL DEFAULT 0"},
		{"origin", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := d.HasColumn(ctx, tx, "repos", c.name)
		if err != nil {
			return fmt.Errorf("%s column: %w", c.name, err)
		}
		if has {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE repos ADD COLUMN `+c.name+` `+c.decl); err != nil {
			return fmt.Errorf("%s column: %w", c.name, err)
		}
	}
	// inbound_conflicts held what the copy-style GitHub importer recorded when an
	// upstream push diverged; the importer is gone (a repository on GitHub is
	// linked, link.go), and so is its table.
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS inbound_conflicts`); err != nil {
		return fmt.Errorf("drop inbound_conflicts: %w", err)
	}
	return nil
}

// bit is a bool as the BIGINT column that holds it.
func bit(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const repoCols = `id,org,project,name,description,default_branch,public,size_bytes,origin,created_at,updated_at`

func scanRepo(sc interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	err := sc.Scan(&r.ID, &r.Org, &r.Project, &r.Name, &r.Description,
		&r.DefaultBranch, &r.Public, &r.SizeBytes, &r.Origin, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// Create inserts a new repo row. Returns errConflict when (org,project,name)
// already exists in the org.
func (s *Store) Create(ctx context.Context, r Repo) error {
	if err := dialect.Text(r.ID, r.Org, r.Project, r.Name, r.Description, r.DefaultBranch, r.Origin); err != nil {
		return fmt.Errorf("insert repo: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (`+repoCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		r.ID, r.Org, r.Project, r.Name, r.Description, r.DefaultBranch,
		bit(r.Public), r.SizeBytes, r.Origin, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errConflict
		}
		return fmt.Errorf("insert repo: %w", err)
	}
	return nil
}

// Get returns the repo for (org,project,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, project, name string) (Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=$1 AND project=$2 AND name=$3`, org, project, name)
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
		`SELECT `+repoCols+` FROM repos WHERE org=$1 AND project=$2 ORDER BY updated_at DESC, name ASC`, org, project)
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
		`SELECT `+repoCols+` FROM repos WHERE org=$1 ORDER BY updated_at DESC, name ASC`, org)
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

// ListPublic returns every PUBLIC repo across all projects for org, newest
// first — the per-org half of the anonymous explore/discovery surface.
func (s *Store) ListPublic(ctx context.Context, org string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=$1 AND public=1 ORDER BY updated_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list public repos: %w", err)
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
		`UPDATE repos SET size_bytes=$1, updated_at=$2 WHERE org=$3 AND project=$4 AND name=$5`,
		sizeBytes, updatedAt, org, project, name)
	if err != nil {
		return fmt.Errorf("set size: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// SetOrigin records a repo's link: the host its history lives on. It does NOT
// bump updated_at: a link is a statement about where the history is, not a
// change to it, and updated_at orders what callers see as activity.
//
// Written by a link whose host let the caller read it (linkTo), never by one
// that was merely asked for.
func (s *Store) SetOrigin(ctx context.Context, org, project, name, origin string) error {
	if err := dialect.Text(origin); err != nil {
		return fmt.Errorf("set origin: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE repos SET origin=$1 WHERE org=$2 AND project=$3 AND name=$4`,
		origin, org, project, name)
	if err != nil {
		return fmt.Errorf("set origin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// SetPublic flips a repo's visibility and bumps updated_at. Public grants
// ANONYMOUS READ (upload-pack) only — receive-pack stays org-authed always.
func (s *Store) SetPublic(ctx context.Context, org, project, name string, public bool, updatedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE repos SET public=$1, updated_at=$2 WHERE org=$3 AND project=$4 AND name=$5`,
		bit(public), updatedAt, org, project, name)
	if err != nil {
		return fmt.Errorf("set public: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// Delete removes a repo row AND cascade-deletes its lifecycle config
// (subscriptions + mirror targets) in one transaction, so a deleted repo can
// never leave an orphaned external mirror target that a re-created repo of the
// same name would silently inherit (Red MED-3: exfil-on-recreate). Reports
// whether the repo row went.
func (s *Store) Delete(ctx context.Context, org, project, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("delete repo: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM repos WHERE org=$1 AND project=$2 AND name=$3`, org, project, name)
	if err != nil {
		return false, fmt.Errorf("delete repo: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repo_subscriptions WHERE org=$1 AND project=$2 AND repo=$3`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo subscriptions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repo_mirrors WHERE org=$1 AND project=$2 AND repo=$3`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo mirrors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pulls WHERE org=$1 AND project=$2 AND repo=$3`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo pulls: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("delete repo: commit: %w", err)
	}
	return n > 0, nil
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
	if err := dialect.Text(v.ID, v.Org, v.Project, v.Repo, v.Channel, v.Events); err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_subscriptions (`+subCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		v.ID, v.Org, v.Project, v.Repo, v.Channel, v.Events, v.CreatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errConflict
		}
		return fmt.Errorf("insert subscription: %w", err)
	}
	return nil
}

// ListSubscriptions returns every subscription for the repo (org, project, repo),
// newest first.
func (s *Store) ListSubscriptions(ctx context.Context, org, project, repo string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+subCols+` FROM repo_subscriptions WHERE org=$1 AND project=$2 AND repo=$3 ORDER BY created_at DESC, id ASC`, org, project, repo)
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

// DeleteSubscription removes a subscription by (org, project, repo, id) — a caller
// may only delete their own org's subscription of the named repo IN SCOPE. Reports
// whether a row went.
func (s *Store) DeleteSubscription(ctx context.Context, org, project, repo, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM repo_subscriptions WHERE org=$1 AND project=$2 AND repo=$3 AND id=$4`, org, project, repo, id)
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
	if err := dialect.Text(v.ID, v.Org, v.Project, v.Repo, v.Host, v.URL); err != nil {
		return fmt.Errorf("insert mirror: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_mirrors (`+mirrorCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		v.ID, v.Org, v.Project, v.Repo, v.Host, v.URL, v.CreatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errConflict
		}
		return fmt.Errorf("insert mirror: %w", err)
	}
	return nil
}

// ListMirrors returns every mirror target for the repo (org, project, repo),
// newest first.
func (s *Store) ListMirrors(ctx context.Context, org, project, repo string) ([]MirrorTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mirrorCols+` FROM repo_mirrors WHERE org=$1 AND project=$2 AND repo=$3 ORDER BY created_at DESC, id ASC`, org, project, repo)
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

// DeleteMirror removes a mirror target by (org, project, repo, id). Reports whether
// a row went.
func (s *Store) DeleteMirror(ctx context.Context, org, project, repo, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM repo_mirrors WHERE org=$1 AND project=$2 AND repo=$3 AND id=$4`, org, project, repo, id)
	if err != nil {
		return false, fmt.Errorf("delete mirror: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── pull requests (propose a branch, merge it) ───────────────────────────────

// The two states a proposal can be in. There is no third: a pull is the
// OUTSTANDING question "may head go into base", and merging is the only thing
// this app does to it. A proposal nobody wants is abandoned by deleting the
// branch, which is how it already worked before there was a row to look at.
const (
	pullOpen   = "open"
	pullMerged = "merged"
)

// Pull is one proposal to move head into base, and the answer to it.
//
// Number is the per-repo handle ("#4"), dense from 1 and allocated by
// CreatePull. Base and Head are BRANCH SHORT NAMES (main, agent/x), never refs
// and never revisions: the branches move while the proposal is open, and a
// proposal that pinned a revision would be answering a question nobody asked by
// the time it is merged. MergedRev is the one revision worth keeping — what base
// pointed at once the merge landed — and is empty while the pull is open.
type Pull struct {
	ID        string
	Org       string
	Project   string
	Repo      string
	Number    int64
	Title     string
	Body      string
	Base      string
	Head      string
	State     string
	Author    string // the validated user who opened it; "" for a caller with no user
	MergedRev string
	CreatedAt int64
	UpdatedAt int64
}

const pullCols = `id,org,project,repo,number,title,body,base,head,state,author,merged_rev,created_at,updated_at`

func scanPull(sc interface{ Scan(...any) error }) (Pull, error) {
	var v Pull
	err := sc.Scan(&v.ID, &v.Org, &v.Project, &v.Repo, &v.Number, &v.Title, &v.Body,
		&v.Base, &v.Head, &v.State, &v.Author, &v.MergedRev, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// CreatePull allocates the next per-repo number and inserts the proposal,
// returning the stored row. errConflict when an OPEN proposal of the same
// (base, head) already exists — the answer a retried agent run gets, and the
// reason a run that proposes twice does not produce two rows to review.
//
// Both statements run in ONE transaction that is the only writer on the store
// (BeginWrite), so the number is read and taken indivisibly: two proposals opened
// at once cannot be handed the same number.
func (s *Store) CreatePull(ctx context.Context, v Pull) (Pull, error) {
	if err := dialect.Text(v.ID, v.Org, v.Project, v.Repo, v.Title, v.Body, v.Base, v.Head, v.State, v.Author, v.MergedRev); err != nil {
		return Pull{}, fmt.Errorf("create pull: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Pull{}, fmt.Errorf("create pull: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var dup int64
	err = tx.QueryRowContext(ctx,
		`SELECT number FROM pulls WHERE org=$1 AND project=$2 AND repo=$3 AND base=$4 AND head=$5 AND state=$6`,
		v.Org, v.Project, v.Repo, v.Base, v.Head, pullOpen).Scan(&dup)
	switch {
	case err == nil:
		return Pull{}, errConflict
	case !errors.Is(err, sql.ErrNoRows):
		return Pull{}, fmt.Errorf("create pull: check open: %w", err)
	}

	var high int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number),0) FROM pulls WHERE org=$1 AND project=$2 AND repo=$3`,
		v.Org, v.Project, v.Repo).Scan(&high); err != nil {
		return Pull{}, fmt.Errorf("create pull: next number: %w", err)
	}
	v.Number = high + 1

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pulls (`+pullCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		v.ID, v.Org, v.Project, v.Repo, v.Number, v.Title, v.Body, v.Base, v.Head,
		v.State, v.Author, v.MergedRev, v.CreatedAt, v.UpdatedAt); err != nil {
		if dialect.Unique(err) {
			return Pull{}, errConflict
		}
		return Pull{}, fmt.Errorf("insert pull: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Pull{}, fmt.Errorf("create pull: commit: %w", err)
	}
	return v, nil
}

// GetPull returns one proposal by its per-repo number, or errNotFound. The
// (org, project, repo) tuple is part of the key, so a number from another
// tenant's repo does not name a row here.
func (s *Store) GetPull(ctx context.Context, org, project, repo string, number int64) (Pull, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pullCols+` FROM pulls WHERE org=$1 AND project=$2 AND repo=$3 AND number=$4`,
		org, project, repo, number)
	v, err := scanPull(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Pull{}, errNotFound
	}
	if err != nil {
		return Pull{}, fmt.Errorf("get pull: %w", err)
	}
	return v, nil
}

// ListPulls returns a repo's proposals, newest number first. An empty state
// returns every one; a state returns only those in it.
func (s *Store) ListPulls(ctx context.Context, org, project, repo, state string) ([]Pull, error) {
	q := `SELECT ` + pullCols + ` FROM pulls WHERE org=$1 AND project=$2 AND repo=$3`
	args := []any{org, project, repo}
	if state != "" {
		q += ` AND state=$4`
		args = append(args, state)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY number DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list pulls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Pull
	for rows.Next() {
		v, err := scanPull(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pull: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkMerged records that base now points at rev because of this proposal, and
// reports whether THIS call is the one that did it.
//
// The `state=open` in the WHERE is the whole point: it makes the transition
// indivisible, so two concurrent merges of one proposal cannot both come back
// having merged it. The caller moves the ref first and calls this second — a
// crash between the two leaves a row that says open about a branch that already
// contains head, which the next merge attempt recognises and settles. The
// reverse order would leave a row claiming a merge that never happened, and that
// is not recoverable by looking at the repository.
func (s *Store) MarkMerged(ctx context.Context, org, project, repo string, number int64, rev string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE pulls SET state=$1, merged_rev=$2, updated_at=$3 WHERE org=$4 AND project=$5 AND repo=$6 AND number=$7 AND state=$8`,
		pullMerged, rev, at, org, project, repo, number, pullOpen)
	if err != nil {
		return false, fmt.Errorf("mark merged: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
