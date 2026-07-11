package social

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
// property, not just a WHERE clause.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS social_accounts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  provider    TEXT NOT NULL DEFAULT 'x',
  handle      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'connected',
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
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_updated  ON social_posts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_status   ON social_posts(org, status);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_schedule ON social_posts(org, schedule_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("social migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ---- accounts ----

// Account is an org-scoped connected social account (a Postiz-style "integration"
// on the live social stack). Provider is the network (x/facebook/instagram/…);
// Status is the connection lifecycle (connected/disconnected/error) — both
// validated at the write layer against the fixed vocabularies in social.go.
type Account struct {
	ID        string `json:"id"`
	Org       string `json:"-"`
	Provider  string `json:"provider"`
	Handle    string `json:"handle"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const accountCols = `id,org,provider,handle,status,created_at,updated_at`

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	err := sc.Scan(&a.ID, &a.Org, &a.Provider, &a.Handle, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (s *Store) CreateAccount(ctx context.Context, a Account) (Account, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_accounts (`+accountCols+`) VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Provider, a.Handle, a.Status, a.CreatedAt, a.UpdatedAt); err != nil {
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	return a, nil
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
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

const postCols = `id,org,content,channel,status,schedule_at,created_at,updated_at`

func scanPost(sc interface{ Scan(...any) error }) (Post, error) {
	var p Post
	err := sc.Scan(&p.ID, &p.Org, &p.Content, &p.Channel, &p.Status, &p.ScheduleAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) CreatePost(ctx context.Context, p Post) (Post, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_posts (`+postCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Content, p.Channel, p.Status, p.ScheduleAt, p.CreatedAt, p.UpdatedAt); err != nil {
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
