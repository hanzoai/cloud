package authors

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	// Mirrors clients/affiliates / clients/referrals — one storage pattern.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers:
//
//	errNotFound → 404, errUnknownRepo → 404, errRepoNotVerified → 404,
//	errInvalidRepo → 400, errInsufficientPending → 400.
var (
	errNotFound            = errors.New("authors: not found")
	errUnknownRepo         = errors.New("authors: unknown repo")
	errRepoNotVerified     = errors.New("authors: repo not verified")
	errInvalidRepo         = errors.New("authors: invalid repo url")
	errInsufficientPending = errors.New("authors: payout exceeds pending earnings")
)

// Status values. An author advances connected → approved (and can be suspended).
// Only an APPROVED author accrues earnings; a connected author can still verify
// repos (ownership proof is independent of program admission).
const (
	StatusConnected = "connected"
	StatusApproved  = "approved"
	StatusSuspended = "suspended"
)

// Verification methods for a repo. "oauth" = the caller's IAM-linked GitHub token
// proved admin permission on the repo; "file" = a hanzo.json on the repo's default
// branch contained the author's verify code (proves default-branch control).
const (
	MethodOAuth = "oauth"
	MethodFile  = "file"
	// MethodMaintainer = the repo is owned by the platform maintainer (owner ∈ the
	// brand's GitHub orgs, e.g. hanzoai / hanzo-*): ownership is intrinsic to the
	// namespace, so a Hanzo-maintained template is auto-verified under the treasury
	// SYSTEM author with no OAuth/file proof — its creator royalty accrues to the
	// Hanzo treasury ("pay ourselves") rather than an external author wallet.
	MethodMaintainer = "maintainer"
)

// Author is one OSS author org enrolled in the deploy-royalty program. Org is
// UNIQUE (one author per org). GithubLogin is the linked GitHub identity; VerifyCode
// is the stable per-author token placed in hanzo.json for the file method. ShareBps
// is the royalty rate in basis points (2000 = 20% of a deploying org's metered spend).
type Author struct {
	ID           string `json:"id"`
	Org          string `json:"-"` // the author's own org; admin view re-exposes it
	GithubLogin  string `json:"githubLogin"`
	VerifyCode   string `json:"-"` // per-author token for the hanzo.json file method
	Status       string `json:"status"`
	ShareBps     int64  `json:"shareBps"`
	AccruedCents int64  `json:"accruedCents"` // lifetime earnings accrued
	PaidCents    int64  `json:"paidCents"`    // lifetime earnings paid out
	CreatedAt    int64  `json:"createdAt"`
	VerifiedAt   int64  `json:"verifiedAt"` // GitHub identity OAuth-verified time (0 = file-only)
	ApprovedAt   int64  `json:"approvedAt"`
	SuspendedAt  int64  `json:"suspendedAt"`
}

// PendingCents is the royalty earned but not yet paid (never negative).
func (a Author) PendingCents() int64 {
	if a.PaidCents >= a.AccruedCents {
		return 0
	}
	return a.AccruedCents - a.PaidCents
}

// AuthorRepo is one repository an author has claimed. RepoURL is the canonical
// host/owner/name form (UNIQUE across the table — a repo belongs to at most one
// author, first-verify wins). Verified flips true once ownership is proven.
type AuthorRepo struct {
	ID         string `json:"id"`
	AuthorID   string `json:"authorId"`
	RepoURL    string `json:"repoUrl"`
	Verified   bool   `json:"verified"`
	Method     string `json:"method"`
	CreatedAt  int64  `json:"createdAt"`
	VerifiedAt int64  `json:"verifiedAt"`
}

// AuthorOrg is one whole OWNER/org an author has claimed (e.g. github.com/acme). A
// verified org claim COVERS every repo under that owner, so any repo the author
// publishes there earns without a per-repo verify. OwnerURL is the canonical
// "host/owner" form (UNIQUE across the table — an owner belongs to at most one author,
// first-verify wins). It sits beside AuthorRepo: a deploy matches a per-repo claim OR
// an owner-wide org claim (per-repo takes precedence).
type AuthorOrg struct {
	ID         string `json:"id"`
	AuthorID   string `json:"authorId"`
	OwnerURL   string `json:"ownerUrl"`
	Verified   bool   `json:"verified"`
	Method     string `json:"method"`
	CreatedAt  int64  `json:"createdAt"`
	VerifiedAt int64  `json:"verifiedAt"`
}

// DeployEvent is one attribution edge: a deploying org deployed a project sourced
// from a verified author repo. UNIQUE(repo_url, project, deploying_org) makes the
// record idempotent (a re-deploy of the same project by the same org is one edge).
type DeployEvent struct {
	ID           string `json:"id"`
	AuthorID     string `json:"authorId"`
	RepoURL      string `json:"repoUrl"`
	Project      string `json:"project"`
	DeployingOrg string `json:"deployingOrg"`
	CreatedAt    int64  `json:"createdAt"`
}

// Accrual is one per-period royalty event: the deploying org's spend for that period
// × the author's share. UNIQUE(author, deploying_org, period) makes the sweep
// at-most-once per period — the royalty latch.
type Accrual struct {
	ID           string `json:"id"`
	AuthorID     string `json:"authorId"`
	DeployingOrg string `json:"deployingOrg"`
	Period       string `json:"period"`
	SpendCents   int64  `json:"spendCents"`
	EarningCents int64  `json:"earningCents"`
	CreatedAt    int64  `json:"createdAt"`
}

// LedgerRow is one immutable, on-chain-ready royalty record appended per latched
// accrual. ComputeProof is nil until a hanzod compute attestation binds the row (a
// follow-up) — it is never fabricated.
type LedgerRow struct {
	ID           string  `json:"id"`
	AuthorID     string  `json:"authorId"`
	DeployingOrg string  `json:"deployingOrg"`
	Period       string  `json:"period"`
	ShareBps     int64   `json:"shareBps"`
	SpendCents   int64   `json:"spendCents"`
	EarningCents int64   `json:"earningCents"`
	ComputeProof *string `json:"computeProof"` // nullable: hanzod attestation (follow-up)
	CreatedAt    int64   `json:"createdAt"`
}

// Payout is one recorded disbursement of accrued royalty. A "credits" method issues
// a commerce grant (Txn set); cash methods (wire/paypal/…) are record-only.
type Payout struct {
	ID          string `json:"id"`
	AuthorID    string `json:"authorId"`
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
	Txn         string `json:"txn,omitempty"`
	// Settlement is where the money landed — settlementTreasury (internal: a
	// first-party author's royalty realized into our own reserve), settlementWallet
	// (a commerce grant to an external author), or settlementCash. Captured at
	// reservation, so the books say plainly which payouts were paid to ourselves.
	Settlement string `json:"settlement,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

// Store is the authors database. ONE SQLite file holds every org's author record,
// claimed repos, deploy-attribution edges, accrual events, and payouts. A
// repo→author lookup is a GLOBAL directory by design (any deploying org presents a
// repo minted by ANY author); every /v1/authors read is scoped by the caller's org
// server-side.
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
CREATE TABLE IF NOT EXISTS authors (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL UNIQUE,
  github_login  TEXT NOT NULL DEFAULT '',
  verify_code   TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  share_bps     INTEGER NOT NULL,
  accrued_cents INTEGER NOT NULL DEFAULT 0,
  paid_cents    INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  verified_at   INTEGER NOT NULL DEFAULT 0,
  approved_at   INTEGER NOT NULL DEFAULT 0,
  suspended_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS author_repos (
  id          TEXT PRIMARY KEY,
  author_id   TEXT NOT NULL,
  repo_url    TEXT NOT NULL UNIQUE,
  verified    INTEGER NOT NULL DEFAULT 0,
  method      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  verified_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_author_repos_author ON author_repos(author_id, created_at);

CREATE TABLE IF NOT EXISTS author_orgs (
  id          TEXT PRIMARY KEY,
  author_id   TEXT NOT NULL,
  owner_url   TEXT NOT NULL UNIQUE,
  verified    INTEGER NOT NULL DEFAULT 0,
  method      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  verified_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_author_orgs_author ON author_orgs(author_id, created_at);

CREATE TABLE IF NOT EXISTS author_deploy_events (
  id            TEXT PRIMARY KEY,
  author_id     TEXT NOT NULL,
  repo_url      TEXT NOT NULL,
  project       TEXT NOT NULL,
  deploying_org TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  UNIQUE(repo_url, project, deploying_org)
);
CREATE INDEX IF NOT EXISTS ix_author_deploys_author ON author_deploy_events(author_id, created_at);

CREATE TABLE IF NOT EXISTS author_accruals (
  id            TEXT PRIMARY KEY,
  author_id     TEXT NOT NULL,
  deploying_org TEXT NOT NULL,
  period        TEXT NOT NULL,
  spend_cents   INTEGER NOT NULL,
  earning_cents INTEGER NOT NULL,
  created_at    INTEGER NOT NULL,
  UNIQUE(author_id, deploying_org, period)
);
CREATE INDEX IF NOT EXISTS ix_author_accruals_author ON author_accruals(author_id, created_at);

CREATE TABLE IF NOT EXISTS author_payouts (
  id           TEXT PRIMARY KEY,
  author_id    TEXT NOT NULL,
  amount_cents INTEGER NOT NULL,
  method       TEXT NOT NULL,
  reference    TEXT NOT NULL DEFAULT '',
  txn          TEXT NOT NULL DEFAULT '',
  -- settlement is WHERE the money landed, captured at reservation time: treasury
  -- (a first-party author's royalty realized into our own reserve — internal
  -- accounting), wallet (a commerce grant into an external author's balance), or
  -- cash (a recorded external disbursement). Stored, never re-derived, so a house
  -- settlement can never later be read as third-party earnings.
  settlement   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_author_payouts_author ON author_payouts(author_id, created_at);

-- APPEND-ONLY, on-chain-ready royalty ledger: one immutable row per latched accrual
-- capturing the per-compute attribution (deploying org's spend → author at share).
-- compute_proof is NULLABLE and reserved for a hanzod compute attestation (a
-- follow-up) — it is written NULL here, never fabricated. Rows are only ever
-- INSERTed, never updated or deleted, so the table is a verifiable ledger.
CREATE TABLE IF NOT EXISTS author_ledger (
  id            TEXT PRIMARY KEY,
  author_id     TEXT NOT NULL,
  deploying_org TEXT NOT NULL,
  period        TEXT NOT NULL,
  share_bps     INTEGER NOT NULL,
  spend_cents   INTEGER NOT NULL,
  earning_cents INTEGER NOT NULL,
  compute_proof TEXT,
  created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_author_ledger_author ON author_ledger(author_id, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("authors migrate: %w", err)
	}
	// Additive column migration for existing databases (SQLite has no ADD COLUMN IF
	// NOT EXISTS): ignore the duplicate-column error. Rows written before this
	// column existed keep the empty default — honestly "unrecorded", never guessed.
	if _, err := s.db.Exec(`ALTER TABLE author_payouts ADD COLUMN settlement TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("authors migrate alter: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ── repo url canonicalization ──────────────────────────────────────────────────

// forgeHosts are the code-forge hosts an author repo can live on: GitHub and GitLab.
// A bare "owner/name" (no host) defaults to the first (github.com).
var forgeHosts = []string{"github.com", "gitlab.com"}

// normalizeTarget reduces any GitHub/GitLab reference to its ONE canonical form and
// reports whether it names a whole OWNER (an org/group) or a specific REPO:
//
//	repo:  "<host>/owner/name"   isOrg=false
//	org:   "<host>/owner"        isOrg=true   (an explicit forge host is REQUIRED)
//
// Lowercased, no scheme, no .git, no trailing slash, no query/fragment, host ∈
// forgeHosts. A bare "owner/name" (no host) still defaults to github.com as a REPO
// (unchanged behavior); a bare single word (no host, no slash) is too ambiguous to be
// an org and returns ("", false). Extra path segments (…/tree/main) collapse to the
// owner/name repo. Returns ("", false) for anything that is neither.
func normalizeTarget(raw string) (canonical string, isOrg bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", false
	}
	// Strip scheme + git@ ssh forms → host/path.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "git@")
	for _, h := range forgeHosts {
		s = strings.Replace(s, h+":", h+"/", 1) // ssh colon → slash
	}
	// Drop query/fragment.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	// Bare "owner/name" (no host) → assume github.com (always a REPO, never an org).
	hasHost := false
	for _, h := range forgeHosts {
		if strings.Contains(s, h+"/") {
			hasHost = true
			break
		}
	}
	if !hasHost && strings.Count(s, "/") == 1 {
		s = "github.com/" + s
	}
	for _, h := range forgeHosts {
		i := strings.Index(s, h+"/")
		if i < 0 {
			continue
		}
		path := strings.Trim(s[i+len(h)+1:], "/")
		if path == "" {
			return "", false
		}
		parts := strings.Split(path, "/")
		if parts[0] == "" {
			return "", false
		}
		if len(parts) == 1 {
			// "<host>/owner" — an owner-wide (org/group) target.
			return h + "/" + parts[0], true
		}
		if parts[1] == "" {
			return "", false
		}
		// "<host>/owner/name[/…]" — a repo target (extra segments ignored).
		name := strings.TrimSuffix(parts[1], ".git")
		return h + "/" + parts[0] + "/" + name, false
	}
	return "", false
}

// normalizeRepo reduces any GitHub/GitLab REPO reference to the canonical
// "<host>/owner/name", or "" for anything that isn't one — including an ORG url, since
// a deploy source is always a concrete repo. BOTH sides of a deploy match go through
// here — the author's claimed repo and hanzo.app's persisted sourceRepo — so
// attribution never misses on a cosmetic URL difference.
func normalizeRepo(raw string) string {
	canonical, isOrg := normalizeTarget(raw)
	if isOrg {
		return ""
	}
	return canonical
}

// splitRepo returns (host, owner, name) for a canonical repo url, or "","","",false.
func splitRepo(canonical string) (host, owner, name string, ok bool) {
	for _, h := range forgeHosts {
		if !strings.HasPrefix(canonical, h+"/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(canonical, h+"/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", false
		}
		return h, parts[0], parts[1], true
	}
	return "", "", "", false
}

// splitOrg returns (host, owner) for a canonical org url "<host>/owner", or "","",false.
func splitOrg(canonical string) (host, owner string, ok bool) {
	for _, h := range forgeHosts {
		if !strings.HasPrefix(canonical, h+"/") {
			continue
		}
		rest := strings.TrimPrefix(canonical, h+"/")
		if rest == "" || strings.Contains(rest, "/") {
			return "", "", false
		}
		return h, rest, true
	}
	return "", "", false
}

// verifyTarget is a parsed "verify" target: either a specific REPO (host/owner/name)
// or a whole OWNER/org (host/owner, name==""). One shape or the other, never both.
type verifyTarget struct {
	host, owner, name string
	canonical         string // "host/owner/name" (repo) or "host/owner" (org)
}

// isOrg reports whether the target is an owner-wide (org) claim rather than one repo.
func (t verifyTarget) isOrg() bool { return t.name == "" }

// parseTarget accepts a user-supplied string — a repo url/owner-name OR an org url —
// and returns the canonical target or errInvalidRepo. It is the ONE entry point the
// verify handler uses; url.Parse guards against absurd inputs.
func parseTarget(raw string) (verifyTarget, error) {
	if len(raw) > 512 {
		return verifyTarget{}, errInvalidRepo
	}
	canonical, isOrg := normalizeTarget(raw)
	if canonical == "" {
		return verifyTarget{}, errInvalidRepo
	}
	// Final sanity parse of the https form.
	if _, err := url.Parse("https://" + canonical); err != nil {
		return verifyTarget{}, errInvalidRepo
	}
	if isOrg {
		host, owner, ok := splitOrg(canonical)
		if !ok {
			return verifyTarget{}, errInvalidRepo
		}
		return verifyTarget{host: host, owner: owner, canonical: canonical}, nil
	}
	host, owner, name, ok := splitRepo(canonical)
	if !ok {
		return verifyTarget{}, errInvalidRepo
	}
	return verifyTarget{host: host, owner: owner, name: name, canonical: canonical}, nil
}

// ── author records ─────────────────────────────────────────────────────────────

const authorCols = `id,org,github_login,verify_code,status,share_bps,accrued_cents,paid_cents,created_at,verified_at,approved_at,suspended_at`

func scanAuthor(sc interface{ Scan(...any) error }) (Author, error) {
	var a Author
	err := sc.Scan(&a.ID, &a.Org, &a.GithubLogin, &a.VerifyCode, &a.Status, &a.ShareBps,
		&a.AccruedCents, &a.PaidCents, &a.CreatedAt, &a.VerifiedAt, &a.ApprovedAt, &a.SuspendedAt)
	return a, err
}

// Connect enrolls org as an author at status=connected, idempotently. It records the
// GitHub login + verify code and, when identityVerified, stamps verified_at. A repeat
// connect UPDATES the login (re-link) but never resets status/accrual/verify_code —
// first connect owns the code. Returns (author, created).
func (s *Store) Connect(ctx context.Context, id, org, githubLogin, verifyCode string, shareBps int64, identityVerified bool, now int64) (Author, bool, error) {
	verifiedAt := int64(0)
	if identityVerified {
		verifiedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO authors (id, org, github_login, verify_code, status, share_bps, created_at, verified_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		id, org, githubLogin, verifyCode, StatusConnected, shareBps, now, verifiedAt)
	if err == nil {
		a, gerr := s.getByID(ctx, id)
		return a, true, gerr
	}
	if !isUnique(err) {
		return Author{}, false, fmt.Errorf("connect: %w", err)
	}
	// Already an author for this org — re-link the login (and stamp verified_at if the
	// re-link proved identity), preserving status/code/accrual.
	if _, uerr := s.db.ExecContext(ctx,
		`UPDATE authors SET github_login=?,
		    verified_at = CASE WHEN ? > 0 THEN ? ELSE verified_at END
		  WHERE org=?`,
		githubLogin, verifiedAt, verifiedAt, org); uerr != nil {
		return Author{}, false, fmt.Errorf("relink: %w", uerr)
	}
	a, gerr := s.GetByOrg(ctx, org)
	return a, false, gerr
}

// EnsureSystemAuthor idempotently seeds a SYSTEM author (the Hanzo treasury
// "pay ourselves" identity) at status=approved, identity-verified, sharing shareBps.
// It owns the platform-maintained OSS templates so their creator royalty accrues to
// the treasury. A repeat call is a no-op that returns the existing row — never
// resetting its accrual, paid, or share. Returns the author keyed by org (org is
// UNIQUE, so one system author per treasury org).
func (s *Store) EnsureSystemAuthor(ctx context.Context, id, org, githubLogin string, shareBps, now int64) (Author, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO authors (id, org, github_login, verify_code, status, share_bps, created_at, verified_at, approved_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		id, org, githubLogin, "", StatusApproved, shareBps, now, now, now)
	if err != nil && !isUnique(err) {
		return Author{}, fmt.Errorf("ensure system author: %w", err)
	}
	if err != nil {
		// The row already exists — ENSURE means ensure. The maintainer org's author
		// IS the system author by definition, so a row left CONNECTED by an earlier
		// manual connect (before this path existed) must still earn; leaving it
		// un-approved silently blocks "pay ourselves" forever. Only that one
		// transition is made: a negotiated share_bps stands, and a SUSPENDED author
		// stays suspended (un-suspending is an operator decision, not a side effect).
		if _, uerr := s.db.ExecContext(ctx,
			`UPDATE authors SET status=?, approved_at=? WHERE org=? AND status=?`,
			StatusApproved, now, org, StatusConnected); uerr != nil {
			return Author{}, fmt.Errorf("approve system author: %w", uerr)
		}
	}
	return s.GetByOrg(ctx, org)
}

func (s *Store) getByID(ctx context.Context, id string) (Author, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+authorCols+` FROM authors WHERE id=?`, id)
	a, err := scanAuthor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, errNotFound
	}
	if err != nil {
		return Author{}, fmt.Errorf("get author: %w", err)
	}
	return a, nil
}

// GetByID re-reads an author by id (post-mutation refresh).
func (s *Store) GetByID(ctx context.Context, id string) (Author, error) { return s.getByID(ctx, id) }

// GetByOrg reads the author record for org, or errNotFound.
func (s *Store) GetByOrg(ctx context.Context, org string) (Author, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+authorCols+` FROM authors WHERE org=?`, org)
	a, err := scanAuthor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, errNotFound
	}
	if err != nil {
		return Author{}, fmt.Errorf("get author by org: %w", err)
	}
	return a, nil
}

// Approve admits an author to earning (status=approved) and optionally overrides the
// share rate. Idempotent. errNotFound if missing.
func (s *Store) Approve(ctx context.Context, id string, shareBps int64, now int64) (Author, error) {
	if _, err := s.getByID(ctx, id); err != nil {
		return Author{}, err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE authors
		    SET status=?,
		        share_bps = CASE WHEN ? > 0 THEN ? ELSE share_bps END,
		        approved_at = CASE WHEN approved_at=0 THEN ? ELSE approved_at END,
		        suspended_at=0
		  WHERE id=?`,
		StatusApproved, shareBps, shareBps, now, id)
	if err != nil {
		return Author{}, fmt.Errorf("approve: %w", err)
	}
	return s.getByID(ctx, id)
}

// Suspend moves an author to suspended (stops NEW accrual; earned royalty is
// unaffected). errNotFound if missing.
func (s *Store) Suspend(ctx context.Context, id string, now int64) (Author, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE authors SET status=?, suspended_at=? WHERE id=?`, StatusSuspended, now, id)
	if err != nil {
		return Author{}, fmt.Errorf("suspend: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Author{}, errNotFound
	}
	return s.getByID(ctx, id)
}

// ListAll returns every author newest-first (the admin directory), bounded.
func (s *Store) ListAll(ctx context.Context, limit int) ([]Author, error) {
	return s.queryAuthors(ctx, `SELECT `+authorCols+` FROM authors ORDER BY created_at DESC LIMIT ?`, limit)
}

// ListApproved returns every approved author (the sweep set), oldest-first.
func (s *Store) ListApproved(ctx context.Context, limit int) ([]Author, error) {
	return s.queryAuthors(ctx, `SELECT `+authorCols+` FROM authors WHERE status=? ORDER BY created_at ASC LIMIT ?`, StatusApproved, limit)
}

func (s *Store) queryAuthors(ctx context.Context, q string, args ...any) ([]Author, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Author, 0, 16)
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, fmt.Errorf("scan author: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── repos ───────────────────────────────────────────────────────────────────────

const repoCols = `id,author_id,repo_url,verified,method,created_at,verified_at`

func scanRepo(sc interface{ Scan(...any) error }) (AuthorRepo, error) {
	var r AuthorRepo
	var verified int
	err := sc.Scan(&r.ID, &r.AuthorID, &r.RepoURL, &verified, &r.Method, &r.CreatedAt, &r.VerifiedAt)
	r.Verified = verified != 0
	return r, err
}

// UpsertVerifiedRepo records repoURL as a VERIFIED repo of authorID, idempotently.
// repoURL is UNIQUE across the table: if it already belongs to THIS author the row is
// refreshed (method/verified_at updated); if it belongs to ANOTHER author the insert
// conflicts and errUnknownRepo-style ownership is refused via errRepoOwned. Returns
// (repo, created).
func (s *Store) UpsertVerifiedRepo(ctx context.Context, id, authorID, repoURL, method string, now int64) (AuthorRepo, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO author_repos (id, author_id, repo_url, verified, method, created_at, verified_at)
		 VALUES (?,?,?,1,?,?,?)`,
		id, authorID, repoURL, method, now, now)
	if err == nil {
		r, gerr := s.getRepoByURL(ctx, repoURL)
		return r, true, gerr
	}
	if !isUnique(err) {
		return AuthorRepo{}, false, fmt.Errorf("upsert repo: %w", err)
	}
	// Repo row exists. Only the OWNING author may (re)verify it.
	existing, gerr := s.getRepoByURL(ctx, repoURL)
	if gerr != nil {
		return AuthorRepo{}, false, gerr
	}
	if existing.AuthorID != authorID {
		return AuthorRepo{}, false, errRepoOwned
	}
	if _, uerr := s.db.ExecContext(ctx,
		`UPDATE author_repos SET verified=1, method=?, verified_at=? WHERE repo_url=?`,
		method, now, repoURL); uerr != nil {
		return AuthorRepo{}, false, fmt.Errorf("re-verify repo: %w", uerr)
	}
	r, gerr := s.getRepoByURL(ctx, repoURL)
	return r, false, gerr
}

// errRepoOwned signals a repo already verified by a DIFFERENT author (409).
var errRepoOwned = errors.New("authors: repo already claimed by another author")

func (s *Store) getRepoByURL(ctx context.Context, repoURL string) (AuthorRepo, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM author_repos WHERE repo_url=?`, repoURL)
	r, err := scanRepo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorRepo{}, errUnknownRepo
	}
	if err != nil {
		return AuthorRepo{}, fmt.Errorf("get repo: %w", err)
	}
	return r, nil
}

// VerifiedRepoForURL resolves a canonical repo url to its VERIFIED owning author +
// repo, or errUnknownRepo / errRepoNotVerified. The deploy-record attribution path.
func (s *Store) VerifiedRepoForURL(ctx context.Context, repoURL string) (AuthorRepo, error) {
	r, err := s.getRepoByURL(ctx, repoURL)
	if err != nil {
		return AuthorRepo{}, err
	}
	if !r.Verified {
		return AuthorRepo{}, errRepoNotVerified
	}
	return r, nil
}

// ListRepos returns an author's claimed repos, newest-first, bounded.
func (s *Store) ListRepos(ctx context.Context, authorID string, limit int) ([]AuthorRepo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoCols+` FROM author_repos WHERE author_id=? ORDER BY created_at DESC LIMIT ?`, authorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]AuthorRepo, 0, 8)
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoCountsByAuthor returns author_id → verified-repo count in ONE GROUP BY (the
// admin directory's per-row count, no N+1 fan-out).
func (s *Store) RepoCountsByAuthor(ctx context.Context) (map[string]int, error) {
	return s.countBy(ctx, `SELECT author_id, COUNT(*) FROM author_repos WHERE verified=1 GROUP BY author_id`)
}

// ── orgs (owner-wide claims) ─────────────────────────────────────────────────────

const orgCols = `id,author_id,owner_url,verified,method,created_at,verified_at`

func scanOrg(sc interface{ Scan(...any) error }) (AuthorOrg, error) {
	var o AuthorOrg
	var verified int
	err := sc.Scan(&o.ID, &o.AuthorID, &o.OwnerURL, &verified, &o.Method, &o.CreatedAt, &o.VerifiedAt)
	o.Verified = verified != 0
	return o, err
}

func (s *Store) getOrgByURL(ctx context.Context, ownerURL string) (AuthorOrg, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+orgCols+` FROM author_orgs WHERE owner_url=?`, ownerURL)
	o, err := scanOrg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorOrg{}, errUnknownRepo
	}
	if err != nil {
		return AuthorOrg{}, fmt.Errorf("get org: %w", err)
	}
	return o, nil
}

// UpsertVerifiedOrg records ownerURL as a VERIFIED owner-wide claim of authorID,
// idempotently. owner_url is UNIQUE: if it already belongs to THIS author the row is
// refreshed (method/verified_at updated); if it belongs to ANOTHER author the insert
// conflicts and ownership is refused via errOrgOwned. Mirrors UpsertVerifiedRepo —
// first-verify wins. Returns (org, created).
func (s *Store) UpsertVerifiedOrg(ctx context.Context, id, authorID, ownerURL, method string, now int64) (AuthorOrg, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO author_orgs (id, author_id, owner_url, verified, method, created_at, verified_at)
		 VALUES (?,?,?,1,?,?,?)`,
		id, authorID, ownerURL, method, now, now)
	if err == nil {
		o, gerr := s.getOrgByURL(ctx, ownerURL)
		return o, true, gerr
	}
	if !isUnique(err) {
		return AuthorOrg{}, false, fmt.Errorf("upsert org: %w", err)
	}
	// Org row exists. Only the OWNING author may (re)verify it.
	existing, gerr := s.getOrgByURL(ctx, ownerURL)
	if gerr != nil {
		return AuthorOrg{}, false, gerr
	}
	if existing.AuthorID != authorID {
		return AuthorOrg{}, false, errOrgOwned
	}
	if _, uerr := s.db.ExecContext(ctx,
		`UPDATE author_orgs SET verified=1, method=?, verified_at=? WHERE owner_url=?`,
		method, now, ownerURL); uerr != nil {
		return AuthorOrg{}, false, fmt.Errorf("re-verify org: %w", uerr)
	}
	o, gerr := s.getOrgByURL(ctx, ownerURL)
	return o, false, gerr
}

// errOrgOwned signals an owner already verified by a DIFFERENT author (409).
var errOrgOwned = errors.New("authors: org already claimed by another author")

// VerifiedOrgForURL resolves a canonical REPO url to a VERIFIED owner-wide claim that
// COVERS it: it derives the repo's "host/owner" and looks up an author_orgs row. This
// is the org arm of deploy attribution — a repo with no per-repo claim still earns when
// its owner is a verified org. Returns errUnknownRepo (no claim) / errRepoNotVerified
// (claim exists but unverified), matching VerifiedRepoForURL so the deploy path treats
// both arms uniformly.
func (s *Store) VerifiedOrgForURL(ctx context.Context, repoURL string) (AuthorOrg, error) {
	host, owner, _, ok := splitRepo(repoURL)
	if !ok {
		return AuthorOrg{}, errUnknownRepo
	}
	o, err := s.getOrgByURL(ctx, host+"/"+owner)
	if err != nil {
		return AuthorOrg{}, err
	}
	if !o.Verified {
		return AuthorOrg{}, errRepoNotVerified
	}
	return o, nil
}

// ListOrgs returns an author's claimed owner-wide orgs, newest-first, bounded.
func (s *Store) ListOrgs(ctx context.Context, authorID string, limit int) ([]AuthorOrg, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+orgCols+` FROM author_orgs WHERE author_id=? ORDER BY created_at DESC LIMIT ?`, authorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]AuthorOrg, 0, 4)
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, fmt.Errorf("scan org: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ── deploy events (attribution edges) ──────────────────────────────────────────

// RecordDeploy records a deploy-attribution edge, idempotently. UNIQUE(repo, project,
// deployingOrg) makes a re-deploy a no-op returning the FIRST edge. Returns (edge,
// created).
func (s *Store) RecordDeploy(ctx context.Context, id, authorID, repoURL, project, deployingOrg string, now int64) (DeployEvent, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO author_deploy_events (id, author_id, repo_url, project, deploying_org, created_at)
		 VALUES (?,?,?,?,?,?)`,
		id, authorID, repoURL, project, deployingOrg, now)
	if err == nil {
		e, gerr := s.getDeploy(ctx, repoURL, project, deployingOrg)
		return e, true, gerr
	}
	if isUnique(err) {
		e, gerr := s.getDeploy(ctx, repoURL, project, deployingOrg)
		return e, false, gerr
	}
	return DeployEvent{}, false, fmt.Errorf("record deploy: %w", err)
}

const deployCols = `id,author_id,repo_url,project,deploying_org,created_at`

func scanDeploy(sc interface{ Scan(...any) error }) (DeployEvent, error) {
	var e DeployEvent
	err := sc.Scan(&e.ID, &e.AuthorID, &e.RepoURL, &e.Project, &e.DeployingOrg, &e.CreatedAt)
	return e, err
}

func (s *Store) getDeploy(ctx context.Context, repoURL, project, deployingOrg string) (DeployEvent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deployCols+` FROM author_deploy_events WHERE repo_url=? AND project=? AND deploying_org=?`,
		repoURL, project, deployingOrg)
	e, err := scanDeploy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DeployEvent{}, errNotFound
	}
	if err != nil {
		return DeployEvent{}, fmt.Errorf("get deploy: %w", err)
	}
	return e, nil
}

// ListDeploys returns an author's deploy events, newest-first, bounded.
func (s *Store) ListDeploys(ctx context.Context, authorID string, limit int) ([]DeployEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deployCols+` FROM author_deploy_events WHERE author_id=? ORDER BY created_at DESC LIMIT ?`, authorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deploys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]DeployEvent, 0, 16)
	for rows.Next() {
		e, err := scanDeploy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deploy: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DistinctDeployingOrgs returns the DISTINCT orgs that have deployed one of an
// author's repos, EXCLUDING the author's own org (no self-royalty). This is the
// accrual set — one accrual per (author, deploying_org, period).
func (s *Store) DistinctDeployingOrgs(ctx context.Context, authorID, authorOrg string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT deploying_org FROM author_deploy_events
		  WHERE author_id=? AND deploying_org<>? ORDER BY deploying_org LIMIT ?`,
		authorID, authorOrg, limit)
	if err != nil {
		return nil, fmt.Errorf("distinct deploying orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, 16)
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("scan deploying org: %w", err)
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// DeployCountsByAuthor returns author_id → deploy-event count in ONE GROUP BY.
func (s *Store) DeployCountsByAuthor(ctx context.Context) (map[string]int, error) {
	return s.countBy(ctx, `SELECT author_id, COUNT(*) FROM author_deploy_events GROUP BY author_id`)
}

func (s *Store) countBy(ctx context.Context, q string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ── accrual latch (the royalty event, at-most-once per period) ─────────────────

// LatchAccrual atomically records ONE accrual for (author, deployingOrg, period),
// adds the earning to the author's accrued balance, AND appends the immutable royalty
// ledger row — all in a single transaction. A UNIQUE violation means this period was
// already accrued (returns false, no error, no double-accrual). Returns won=true only
// when THIS call created the accrual. ledgerID/shareBps stamp the ledger row; its
// compute_proof is written NULL (a hanzod attestation is a follow-up).
func (s *Store) LatchAccrual(ctx context.Context, accrualID, ledgerID, authorID, deployingOrg, period string, shareBps, spendCents, earningCents, now int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("accrual tx: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO author_accruals (id, author_id, deploying_org, period, spend_cents, earning_cents, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		accrualID, authorID, deployingOrg, period, spendCents, earningCents, now)
	if err != nil {
		_ = tx.Rollback()
		if isUnique(err) {
			return false, nil // already accrued this period — idempotent
		}
		return false, fmt.Errorf("insert accrual: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE authors SET accrued_cents = accrued_cents + ? WHERE id=?`, earningCents, authorID); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("accrue balance: %w", err)
	}
	// Append the immutable ledger row (compute_proof NULL — the attestation is a
	// follow-up). Committed with the accrual so the ledger never diverges from balance.
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO author_ledger (id, author_id, deploying_org, period, share_bps, spend_cents, earning_cents, compute_proof, created_at)
		 VALUES (?,?,?,?,?,?,?,NULL,?)`,
		ledgerID, authorID, deployingOrg, period, shareBps, spendCents, earningCents, now); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("append ledger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("accrual commit: %w", err)
	}
	return true, nil
}

// ── the royalty ledger (append-only, on-chain-ready) ───────────────────────────

const ledgerCols = `id,author_id,deploying_org,period,share_bps,spend_cents,earning_cents,compute_proof,created_at`

func scanLedger(sc interface{ Scan(...any) error }) (LedgerRow, error) {
	var r LedgerRow
	err := sc.Scan(&r.ID, &r.AuthorID, &r.DeployingOrg, &r.Period, &r.ShareBps,
		&r.SpendCents, &r.EarningCents, &r.ComputeProof, &r.CreatedAt)
	return r, err
}

// ListLedger returns an author's royalty ledger rows, newest-first, bounded. period
// narrows to ONE accrual bucket (the YYYY-MM periodKey mints); "" is every period.
func (s *Store) ListLedger(ctx context.Context, authorID, period string, limit int) ([]LedgerRow, error) {
	q := `SELECT ` + ledgerCols + ` FROM author_ledger WHERE author_id=?`
	args := []any{authorID}
	if period != "" {
		q += ` AND period=?`
		args = append(args, period)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]LedgerRow, 0, 16)
	for rows.Next() {
		r, err := scanLedger(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LedgerTotals foots the WHOLE ledger — every row, every period. The reconciliation
// it feeds must never be computed from a paged window, or a truncated read would
// silently claim the books balance.
func (s *Store) LedgerTotals(ctx context.Context, authorID string) (rows int, earningCents int64, err error) {
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(earning_cents),0) FROM author_ledger WHERE author_id=?`,
		authorID).Scan(&rows, &earningCents); err != nil {
		return 0, 0, fmt.Errorf("ledger totals: %w", err)
	}
	return rows, earningCents, nil
}

// AuthorsDeployedBy returns the DISTINCT APPROVED authors whose VERIFIED repo the
// given org deployed, EXCLUDING any author whose own org is the deploying org (no
// self-royalty). This is the per-compute attribution set the unified accrual walk
// folds over for one source org.
func (s *Store) AuthorsDeployedBy(ctx context.Context, deployingOrg string, limit int) ([]Author, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT `+authorColsPrefixed("a")+`
		   FROM authors a
		   JOIN author_deploy_events d ON d.author_id = a.id
		   JOIN author_repos r ON r.repo_url = d.repo_url AND r.author_id = a.id AND r.verified = 1
		  WHERE d.deploying_org = ? AND a.org <> ? AND a.status = ?
		  ORDER BY a.created_at ASC LIMIT ?`,
		deployingOrg, deployingOrg, StatusApproved, limit)
	if err != nil {
		return nil, fmt.Errorf("authors deployed by %q: %w", deployingOrg, err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Author, 0, 8)
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, fmt.Errorf("scan author: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// authorColsPrefixed returns authorCols with each column prefixed by the table alias,
// for a joined SELECT that must disambiguate columns.
func authorColsPrefixed(alias string) string {
	cols := strings.Split(authorCols, ",")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ",")
}

// ── payouts ───────────────────────────────────────────────────────────────────

// RecordPayout atomically RESERVES amountCents against the author's pending royalty
// (accrued − paid) and records a payout row — in one transaction. The WHERE guard
// makes it impossible to pay out more than is owed, even under concurrency
// (RowsAffected 0 → errInsufficientPending). The commerce grant (credits payout)
// happens AFTER, outside the tx; SetPayoutTxn records the receipt. errNotFound if the
// author is missing.
func (s *Store) RecordPayout(ctx context.Context, payoutID, authorID string, amountCents int64, method, reference, settlement string, now int64) (Payout, error) {
	if amountCents <= 0 {
		return Payout{}, errInsufficientPending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Payout{}, fmt.Errorf("payout tx: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE authors SET paid_cents = paid_cents + ?
		  WHERE id=? AND (accrued_cents - paid_cents) >= ?`,
		amountCents, authorID, amountCents)
	if err != nil {
		_ = tx.Rollback()
		return Payout{}, fmt.Errorf("reserve payout: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		_ = tx.Rollback()
		if _, gerr := s.getByID(ctx, authorID); gerr == errNotFound {
			return Payout{}, errNotFound
		}
		return Payout{}, errInsufficientPending
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO author_payouts (id, author_id, amount_cents, method, reference, settlement, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		payoutID, authorID, amountCents, method, reference, settlement, now); err != nil {
		_ = tx.Rollback()
		return Payout{}, fmt.Errorf("insert payout: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Payout{}, fmt.Errorf("payout commit: %w", err)
	}
	return Payout{ID: payoutID, AuthorID: authorID, AmountCents: amountCents, Method: method, Reference: reference, Settlement: settlement, CreatedAt: now}, nil
}

// VoidPayout reverses a RecordPayout that could not be BACKED by the treasury
// reserve: it deletes the payout row and restores the reserved amount to pending
// (paid_cents −= amount), in one transaction. It is the compensating action when the
// fund cannot cover a payout the pending-guard already reserved — so a blocked payout
// leaves the author's pending royalty intact, honestly, instead of silently burning it.
func (s *Store) VoidPayout(ctx context.Context, payoutID, authorID string, amountCents int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("void tx: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM author_payouts WHERE id=?`, payoutID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete payout: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE authors SET paid_cents = paid_cents - ? WHERE id=?`, amountCents, authorID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("restore pending: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("void commit: %w", err)
	}
	return nil
}

// SetPayoutTxn records the commerce ledger transaction id after a credits payout
// deposit lands (best-effort receipt; the pending reservation is the authority).
func (s *Store) SetPayoutTxn(ctx context.Context, payoutID, txn string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE author_payouts SET txn=? WHERE id=?`, txn, payoutID)
	if err != nil {
		return fmt.Errorf("set payout txn: %w", err)
	}
	return nil
}

const payoutCols = `id,author_id,amount_cents,method,reference,txn,settlement,created_at`

func scanPayout(sc interface{ Scan(...any) error }) (Payout, error) {
	var p Payout
	err := sc.Scan(&p.ID, &p.AuthorID, &p.AmountCents, &p.Method, &p.Reference, &p.Txn, &p.Settlement, &p.CreatedAt)
	return p, err
}

// ListPayouts returns an author's payout history, newest-first, bounded.
func (s *Store) ListPayouts(ctx context.Context, authorID string, limit int) ([]Payout, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+payoutCols+` FROM author_payouts WHERE author_id=? ORDER BY created_at DESC LIMIT ?`, authorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list payouts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Payout, 0, 8)
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payout: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// isUnique reports whether err is a SQLite UNIQUE/PRIMARY-KEY constraint violation
// (the idempotency + collision signal), matched on message text so it holds under
// BOTH the cgo and pure-Go drivers.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "constraint failed") || strings.Contains(m, "primary key")
}
