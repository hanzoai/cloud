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
	Public        bool // public repos allow ANONYMOUS read (upload-pack); writes stay org-authed
	SizeBytes     int64
	// Origin is the URL this repo's objects can be fetched from again, recorded
	// by the mirror that brought them in. Empty means NOBODY KNOWS where these
	// bytes came from, and that is the whole of the eviction rule in reclaim.go:
	// a copy with an origin can be made again, so releasing it costs a refetch;
	// a copy without one is the only copy there is.
	//
	// It lives on the ROW and not in the repo's git config because eviction
	// deletes the directory the config is in. A fact that dies with the thing it
	// describes cannot be the reason it was safe to delete it.
	Origin    string
	CreatedAt int64
	UpdatedAt int64
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
  public         INTEGER NOT NULL DEFAULT 0,
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  origin         TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
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
  created_at INTEGER NOT NULL
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
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_repo_mirrors ON repo_mirrors(org, project, repo, host);
CREATE INDEX IF NOT EXISTS ix_repo_mirrors_repo ON repo_mirrors(org, project, repo);

-- inbound_conflicts records the ONE thing an inbound sync must persist: a branch
-- whose upstream (GitHub) push DIVERGED from native so the fast-forward-only fetch
-- was REJECTED and native (canonical) was left unchanged. A repo with no rows here
-- is "synced"; a row is a split-brain the console surfaces + an operator resolves.
-- Cleared when a later ff-apply (or a full re-import) reconciles the branch. Keyed
-- on the full (org, project, repo, branch); cascade-deleted with the repo.
CREATE TABLE IF NOT EXISTS inbound_conflicts (
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  branch     TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, project, repo, branch)
);
CREATE INDEX IF NOT EXISTS ix_inbound_conflicts_repo ON inbound_conflicts(org, project, repo);

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
  number     INTEGER NOT NULL,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  base       TEXT NOT NULL,
  head       TEXT NOT NULL,
  state      TEXT NOT NULL DEFAULT 'open',
  author     TEXT NOT NULL DEFAULT '',
  merged_rev TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pulls_number ON pulls(org, project, repo, number);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pulls_open ON pulls(org, project, repo, base, head) WHERE state='open';
CREATE INDEX IF NOT EXISTS ix_pulls_repo ON pulls(org, project, repo, state);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// public: visibility bit added after the initial schema. Fresh DBs get it
	// from the CREATE TABLE above; pre-existing DBs gain it here. The duplicate-
	// column error on fresh DBs is the expected no-op.
	if _, err := s.db.Exec(`ALTER TABLE repos ADD COLUMN public INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate public column: %w", err)
	}
	// origin: added with the bound in reclaim.go. It defaults EMPTY, so every
	// repo that predates this column is pinned until somebody says where it came
	// from — which is the safe direction, and the only one available: the bytes
	// on disk carry no remote, so the migration cannot infer an answer.
	if _, err := s.db.Exec(`ALTER TABLE repos ADD COLUMN origin TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate origin column: %w", err)
	}
	return nil
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (`+repoCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.Project, r.Name, r.Description, r.DefaultBranch,
		r.Public, r.SizeBytes, r.Origin, r.CreatedAt, r.UpdatedAt)
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

// ListPublic returns every PUBLIC repo across all projects for org, newest
// first — the per-org half of the anonymous explore/discovery surface.
func (s *Store) ListPublic(ctx context.Context, org string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE org=? AND public=1 ORDER BY updated_at DESC, name ASC`, org)
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

// SetOrigin records where a repo's objects can be fetched from again. It does
// NOT bump updated_at: recording an origin is a statement about the copy, not a
// change to the repository, and updated_at orders what callers see as activity.
//
// Written by a mirror that SUCCEEDED, never by one that was merely asked for, so
// the claim "this can be fetched again" is one that was just demonstrated rather
// than one a caller made. That is the difference between a cache entry and a
// hope.
func (s *Store) SetOrigin(ctx context.Context, org, project, name, origin string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE repos SET origin=? WHERE org=? AND project=? AND name=?`,
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
		`UPDATE repos SET public=?, updated_at=? WHERE org=? AND project=? AND name=?`,
		public, updatedAt, org, project, name)
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
		`DELETE FROM repos WHERE org=? AND project=? AND name=?`, org, project, name)
	if err != nil {
		return false, fmt.Errorf("delete repo: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repo_subscriptions WHERE org=? AND project=? AND repo=?`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo subscriptions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repo_mirrors WHERE org=? AND project=? AND repo=?`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo mirrors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM inbound_conflicts WHERE org=? AND project=? AND repo=?`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo inbound conflicts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pulls WHERE org=? AND project=? AND repo=?`, org, project, name); err != nil {
		return false, fmt.Errorf("delete repo pulls: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("delete repo: commit: %w", err)
	}
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

// ListSubscriptions returns every subscription for the repo (org, project, repo),
// newest first.
func (s *Store) ListSubscriptions(ctx context.Context, org, project, repo string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+subCols+` FROM repo_subscriptions WHERE org=? AND project=? AND repo=? ORDER BY created_at DESC, id ASC`, org, project, repo)
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
		`DELETE FROM repo_subscriptions WHERE org=? AND project=? AND repo=? AND id=?`, org, project, repo, id)
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

// ListMirrors returns every mirror target for the repo (org, project, repo),
// newest first.
func (s *Store) ListMirrors(ctx context.Context, org, project, repo string) ([]MirrorTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mirrorCols+` FROM repo_mirrors WHERE org=? AND project=? AND repo=? ORDER BY created_at DESC, id ASC`, org, project, repo)
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
		`DELETE FROM repo_mirrors WHERE org=? AND project=? AND repo=? AND id=?`, org, project, repo, id)
	if err != nil {
		return false, fmt.Errorf("delete mirror: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── inbound-sync conflicts (GitHub→native fast-forward rejections) ───────────

// RecordConflict upserts the divergence marker for one branch: the upstream push
// could not fast-forward native, so native was preserved and this row records the
// split-brain for the console + operator. Upsert (not insert) so a repeated
// diverging push refreshes the detail/timestamp instead of erroring.
func (s *Store) RecordConflict(ctx context.Context, org, project, repo, branch, detail string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_conflicts (org,project,repo,branch,detail,updated_at) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org,project,repo,branch) DO UPDATE SET detail=excluded.detail, updated_at=excluded.updated_at`,
		org, project, repo, branch, detail, at)
	if err != nil {
		return fmt.Errorf("record inbound conflict: %w", err)
	}
	return nil
}

// ClearConflict removes one branch's divergence marker — a later ff-apply
// reconciled it. Idempotent (no row ⇒ no-op).
func (s *Store) ClearConflict(ctx context.Context, org, project, repo, branch string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM inbound_conflicts WHERE org=? AND project=? AND repo=? AND branch=?`,
		org, project, repo, branch)
	if err != nil {
		return fmt.Errorf("clear inbound conflict: %w", err)
	}
	return nil
}

// ClearRepoConflicts removes every branch's divergence marker for a repo — a full
// re-import force-fetches every ref, reconciling the repo wholesale. Idempotent.
func (s *Store) ClearRepoConflicts(ctx context.Context, org, project, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM inbound_conflicts WHERE org=? AND project=? AND repo=?`, org, project, repo)
	if err != nil {
		return fmt.Errorf("clear repo inbound conflicts: %w", err)
	}
	return nil
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
// Both statements run in ONE transaction against a single-connection store, so
// the number is read and taken indivisibly: two proposals opened at once cannot
// be handed the same number.
func (s *Store) CreatePull(ctx context.Context, v Pull) (Pull, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Pull{}, fmt.Errorf("create pull: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var dup int64
	err = tx.QueryRowContext(ctx,
		`SELECT number FROM pulls WHERE org=? AND project=? AND repo=? AND base=? AND head=? AND state=?`,
		v.Org, v.Project, v.Repo, v.Base, v.Head, pullOpen).Scan(&dup)
	switch {
	case err == nil:
		return Pull{}, errConflict
	case !errors.Is(err, sql.ErrNoRows):
		return Pull{}, fmt.Errorf("create pull: check open: %w", err)
	}

	var high int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number),0) FROM pulls WHERE org=? AND project=? AND repo=?`,
		v.Org, v.Project, v.Repo).Scan(&high); err != nil {
		return Pull{}, fmt.Errorf("create pull: next number: %w", err)
	}
	v.Number = high + 1

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pulls (`+pullCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Org, v.Project, v.Repo, v.Number, v.Title, v.Body, v.Base, v.Head,
		v.State, v.Author, v.MergedRev, v.CreatedAt, v.UpdatedAt); err != nil {
		if isUnique(err) {
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
		`SELECT `+pullCols+` FROM pulls WHERE org=? AND project=? AND repo=? AND number=?`,
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
	q := `SELECT ` + pullCols + ` FROM pulls WHERE org=? AND project=? AND repo=?`
	args := []any{org, project, repo}
	if state != "" {
		q += ` AND state=?`
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
		`UPDATE pulls SET state=?, merged_rev=?, updated_at=? WHERE org=? AND project=? AND repo=? AND number=? AND state=?`,
		pullMerged, rev, at, org, project, repo, number, pullOpen)
	if err != nil {
		return false, fmt.Errorf("mark merged: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ConflictRepoSet returns the set of repos (by name) with ≥1 unresolved inbound
// conflict in (org, project) — one query backing the repo-list status roll-up.
func (s *Store) ConflictRepoSet(ctx context.Context, org, project string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT repo FROM inbound_conflicts WHERE org=? AND project=?`, org, project)
	if err != nil {
		return nil, fmt.Errorf("list conflict repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("scan conflict repo: %w", err)
		}
		out[repo] = true
	}
	return out, rows.Err()
}
