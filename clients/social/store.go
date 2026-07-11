package social

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
	// panic at init. Blank import registers the driver. Mirrors clients/crm.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409.
var (
	errNotFound = errors.New("social: not found")
	errConflict = errors.New("social: already exists")
)

// Store is the social database. ONE SQLite file ({DataDir}/social.db) holds every
// org's records; tenant isolation is the `org` column, enforced on EVERY query.
// This mirrors clients/crm exactly (the ONE storage pattern). MaxOpenConns(1)
// serializes writes against the single-writer file.
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

// migrate creates the accounts + posts tables. Idempotent (IF NOT EXISTS). Each
// table leads its lookup indexes with `org` so tenant isolation is a physical
// property, not just a WHERE clause. The one exception is
// ix_social_posts_status_schedule: the scheduler's due-post sweep is the ONE
// system-level (cross-org) read (see DueScheduled), so it is led by (status,
// schedule_at), never by org.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS social_accounts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  provider    TEXT NOT NULL DEFAULT 'x',
  handle      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'connected',
  token       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_accounts_org_updated  ON social_accounts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_social_accounts_org_provider ON social_accounts(org, provider);

CREATE TABLE IF NOT EXISTS social_posts (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  content      TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL DEFAULT 'x',
  status       TEXT NOT NULL DEFAULT 'draft',
  schedule_at  INTEGER NOT NULL DEFAULT 0,
  account_id   TEXT NOT NULL DEFAULT '',
  external_id  TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_updated       ON social_posts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_status        ON social_posts(org, status);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_schedule      ON social_posts(org, schedule_at);
CREATE INDEX IF NOT EXISTS ix_social_posts_status_schedule   ON social_posts(status, schedule_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("social migrate: %w", err)
	}
	// Idempotent column upgrades for a pre-existing DB created before publishing
	// landed (fresh installs already have them via the DDL above). ADD COLUMN on a
	// column that already exists is the only error swallowed.
	for _, ac := range []struct{ table, col, def string }{
		{"social_accounts", "token", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "account_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "external_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "error", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumn(ac.table, ac.col, ac.def); err != nil {
			return err
		}
	}
	return nil
}

// addColumn adds a column to an existing table, treating an already-present
// column as success (SQLite has no ADD COLUMN IF NOT EXISTS).
func (s *Store) addColumn(table, col, def string) error {
	_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def))
	if err == nil || strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("social migrate: add %s.%s: %w", table, col, err)
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ---- accounts ----

// Account is an org-scoped connected social account (a Postiz-style "integration"
// on the live social stack). Provider is the network (x/facebook/instagram/…);
// Status is the connection lifecycle (connected/disconnected/error) — both
// validated at the write layer against the fixed vocabularies in social.go.
type Account struct {
	ID       string `json:"id"`
	Org      string `json:"-"`
	Provider string `json:"provider"`
	Handle   string `json:"handle"`
	Status   string `json:"status"`
	// Token is the account's provider access token (obtained by the connect/OAuth
	// flow). It is a secret at rest — the store is SQLCipher-encrypted under CGO —
	// and is NEVER serialized to an API response (`json:"-"`), so a token can never
	// leak to the browser. Only the publisher reads it. The live Postiz stack stores
	// this token in PLAINTEXT in Postgres; this fold keeps it encrypted at rest.
	Token     string `json:"-"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const accountCols = `id,org,provider,handle,status,token,created_at,updated_at`

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	err := sc.Scan(&a.ID, &a.Org, &a.Provider, &a.Handle, &a.Status, &a.Token, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (s *Store) CreateAccount(ctx context.Context, a Account) (Account, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_accounts (`+accountCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Provider, a.Handle, a.Status, a.Token, a.CreatedAt, a.UpdatedAt); err != nil {
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	return a, nil
}

// ListConnectedAccounts returns an org's CONNECTED accounts for one provider — the
// publish targets for a post on that channel. status='connected' only (a disconnected
// or errored account is never a publish target). Org-scoped like every other read.
func (s *Store) ListConnectedAccounts(ctx context.Context, org, provider string) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountCols+` FROM social_accounts WHERE org=? AND provider=? AND status='connected' ORDER BY updated_at DESC`,
		org, provider)
	if err != nil {
		return nil, fmt.Errorf("list connected accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Account, 0, 4)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAccount(ctx context.Context, org, id string) (Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM social_accounts WHERE org=? AND id=?`, org, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, errNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("get account: %w", err)
	}
	return a, nil
}

// ListAccounts lists the org's accounts, optionally filtered by provider
// (provider=="" means all). Most-recently-updated first.
func (s *Store) ListAccounts(ctx context.Context, org, provider string, limit int) ([]Account, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if provider == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+accountCols+` FROM social_accounts WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+accountCols+` FROM social_accounts WHERE org=? AND provider=? ORDER BY updated_at DESC LIMIT ?`, org, provider, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Account, 0, 16)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAccount(ctx context.Context, a Account) (Account, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_accounts SET provider=?,handle=?,status=?,updated_at=? WHERE org=? AND id=?`,
		a.Provider, a.Handle, a.Status, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Account{}, errNotFound
	}
	return s.GetAccount(ctx, a.Org, a.ID)
}

func (s *Store) DeleteAccount(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM social_accounts WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete account: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- posts ----

// Post is an org-scoped social post. Channel is the target network
// (x/facebook/instagram/…); Status is the lifecycle (draft/scheduled/published/
// failed). ScheduleAt is a unix timestamp (0 = not scheduled / publish now) — a
// post with Status=="scheduled" carries the future ScheduleAt. All validated at
// the write layer against the fixed vocabularies in social.go.
type Post struct {
	ID         string `json:"id"`
	Org        string `json:"-"`
	Content    string `json:"content"`
	Channel    string `json:"channel"`
	Status     string `json:"status"`
	ScheduleAt int64  `json:"scheduleAt"`
	// AccountID / ExternalID / Error are server-managed publish results, set only by
	// the publish path (never by a client update): the account a post was published
	// through, the provider's returned external post id (for reconciliation), and the
	// last failure reason. Empty until a publish attempt lands.
	AccountID  string `json:"accountId,omitempty"`
	ExternalID string `json:"externalId,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

const postCols = `id,org,content,channel,status,schedule_at,account_id,external_id,error,created_at,updated_at`

func scanPost(sc interface{ Scan(...any) error }) (Post, error) {
	var p Post
	err := sc.Scan(&p.ID, &p.Org, &p.Content, &p.Channel, &p.Status, &p.ScheduleAt,
		&p.AccountID, &p.ExternalID, &p.Error, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) CreatePost(ctx context.Context, p Post) (Post, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_posts (`+postCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Content, p.Channel, p.Status, p.ScheduleAt,
		p.AccountID, p.ExternalID, p.Error, p.CreatedAt, p.UpdatedAt); err != nil {
		return Post{}, fmt.Errorf("insert post: %w", err)
	}
	return p, nil
}

func (s *Store) GetPost(ctx context.Context, org, id string) (Post, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+postCols+` FROM social_posts WHERE org=? AND id=?`, org, id)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, errNotFound
	}
	if err != nil {
		return Post{}, fmt.Errorf("get post: %w", err)
	}
	return p, nil
}

// ListPosts lists the org's posts, optionally filtered by status (status=="" means
// all). Most-recently-updated first.
func (s *Store) ListPosts(ctx context.Context, org, status string, limit int) ([]Post, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+postCols+` FROM social_posts WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+postCols+` FROM social_posts WHERE org=? AND status=? ORDER BY updated_at DESC LIMIT ?`, org, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list posts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Post, 0, 16)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePost(ctx context.Context, p Post) (Post, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET content=?,channel=?,status=?,schedule_at=?,updated_at=? WHERE org=? AND id=?`,
		p.Content, p.Channel, p.Status, p.ScheduleAt, p.UpdatedAt, p.Org, p.ID)
	if err != nil {
		return Post{}, fmt.Errorf("update post: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Post{}, errNotFound
	}
	return s.GetPost(ctx, p.Org, p.ID)
}

func (s *Store) DeletePost(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM social_posts WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete post: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- publish state machine ----

// ClaimForPublish atomically claims a post for a publish attempt: it flips a
// publishable post (draft/scheduled/failed) to the transient 'publishing' state and
// returns it. This is the concurrency guard shared by the HTTP publish handler and
// the scheduler — with SQLite's single writer, exactly one caller's UPDATE affects a
// given row, so a post is published AT MOST once even if the handler and a scheduler
// tick race for it. Returns (post, true) when THIS caller won the claim, (post,
// false) when the post is not claimable (already published, or being published by
// another caller), and errNotFound if the post is not the org's.
func (s *Store) ClaimForPublish(ctx context.Context, org, id string, now int64) (Post, bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='publishing',updated_at=? WHERE org=? AND id=? AND status IN ('draft','scheduled','failed')`,
		now, org, id)
	if err != nil {
		return Post{}, false, fmt.Errorf("claim post: %w", err)
	}
	claimed := int64(0)
	if n, _ := res.RowsAffected(); n > 0 {
		claimed = n
	}
	p, err := s.GetPost(ctx, org, id) // errNotFound distinguishes absent from not-claimable
	if err != nil {
		return Post{}, false, err
	}
	return p, claimed > 0, nil
}

// MarkPublished records a successful publish: status→published, the account it went
// through, the provider's external post id, and clears any prior error. Org-scoped.
func (s *Store) MarkPublished(ctx context.Context, org, id, accountID, externalID string, now int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='published',account_id=?,external_id=?,error='',updated_at=? WHERE org=? AND id=?`,
		accountID, externalID, now, org, id); err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}

// MarkFailed records a failed publish: status→failed with an honest reason (retryable
// — a failed post can be claimed again). Org-scoped.
func (s *Store) MarkFailed(ctx context.Context, org, id, reason string, now int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='failed',error=?,updated_at=? WHERE org=? AND id=?`,
		reason, now, org, id); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// OrgPost is the minimal (org,id) identity the scheduler's due sweep returns — enough
// to dispatch each due post back into the strictly org-scoped publish path.
type OrgPost struct {
	Org string
	ID  string
}

// DueScheduled returns up to `limit` posts whose schedule time has arrived
// (status='scheduled' AND schedule_at <= now), oldest-due first. This is the SINGLE
// system-level (cross-org) read in the subsystem — the scheduler's due sweep. It reads
// only the identifiers needed to dispatch; the actual publish re-enters the org-scoped
// path (publishPost), so tenant isolation is preserved: the sweep dispatches, it never
// returns or mutates one org's content under another's request. A 'scheduled' post with
// an unset time (schedule_at=0) is due immediately — the durable backstop for the
// on-create fanout.
func (s *Store) DueScheduled(ctx context.Context, now int64, limit int) ([]OrgPost, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,id FROM social_posts WHERE status='scheduled' AND schedule_at<=? ORDER BY schedule_at LIMIT ?`,
		now, limit)
	if err != nil {
		return nil, fmt.Errorf("due scheduled: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]OrgPost, 0, 16)
	for rows.Next() {
		var o OrgPost
		if err := rows.Scan(&o.Org, &o.ID); err != nil {
			return nil, fmt.Errorf("scan due: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecoverStuckPublishing resets any post left in the transient 'publishing' state by a
// crash mid-attempt back to 'failed' (retryable). It is deliberately NOT reset to
// 'published': we cannot know whether the provider actually received the post, and a
// false 'published' would silently drop it, whereas a false 'failed' is a safe,
// visible retry. Runs once at Mount; cross-org recovery sweep; returns the count reset.
func (s *Store) RecoverStuckPublishing(ctx context.Context, now int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='failed',error='interrupted before completion',updated_at=? WHERE status='publishing'`,
		now)
	if err != nil {
		return 0, fmt.Errorf("recover stuck: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- summary ----

// Counts returns the per-org roll-up: total posts, how many are scheduled, how many
// are published, and the number of connected accounts — a real, non-fabricated
// summary for the social module's overview cards.
func (s *Store) Counts(ctx context.Context, org string) (posts, scheduled, published, accounts int, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status='scheduled' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(CASE WHEN status='published' THEN 1 ELSE 0 END),0)
		 FROM social_posts WHERE org=?`, org)
	if err = row.Scan(&posts, &scheduled, &published); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count posts: %w", err)
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM social_accounts WHERE org=?`, org).Scan(&accounts); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count accounts: %w", err)
	}
	return posts, scheduled, published, accounts, nil
}
