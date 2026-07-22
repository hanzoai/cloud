// The opt-in preference store: the durable, OPT-IN-BY-DEFAULT-PRIVATE half of the
// leaderboard. Two tiny tenant-keyed tables in one Hanzo Base/SQLite file — the same
// eval/settings discipline (cek.Open, MaxOpenConns(1) to serialize writes against the
// file lock, a mandatory key predicate on every statement).
//
//   - user_optin  (user_id PK, org, handle, listed): a user opts THEMSELVES into
//     public listing with a chosen handle. Default absent ⇒ NOT listed (private):
//     the user still sees their own rank, but is never named to others.
//   - org_optin   (org PK, display, listed): an org admin opts the ORG into the
//     public global board with a display name. Default absent ⇒ private.
//
// Isolation: a user row is written only for the CALLER's own user_id; the listing
// read is org-scoped (`WHERE org=? AND listed=1`). An org row is written only by an
// admin of that org; the global-listing read is `WHERE listed=1` (org-level display
// only — no user data). No secret ever lands here.
package leaderboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite" // registers the "sqlite" database/sql driver
)

var errNotFound = errors.New("leaderboard: not found")

// userOptin is a user's public-listing preference. Listed=false ⇒ private (default).
type userOptin struct {
	UserID    string
	Org       string
	Handle    string
	Listed    bool
	UpdatedAt int64
	CreatedAt int64
}

// orgOptin is an org's public-board preference.
type orgOptin struct {
	Org       string
	Display   string
	Listed    bool
	UpdatedAt int64
	CreatedAt int64
}

// optinStore is the metastore over one SQLite file ({DataDir}/leaderboard.db).
type optinStore struct {
	db *sql.DB
}

func openOptinStore(path string) (*optinStore, error) {
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
	s := &optinStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *optinStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS user_optin (
  user_id    TEXT NOT NULL PRIMARY KEY,
  org        TEXT NOT NULL,
  handle     TEXT NOT NULL DEFAULT '',
  listed     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_user_optin_org ON user_optin(org, listed);
CREATE TABLE IF NOT EXISTS org_optin (
  org        TEXT NOT NULL PRIMARY KEY,
  display    TEXT NOT NULL DEFAULT '',
  listed     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_org_optin_listed ON org_optin(listed);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *optinStore) Close() error { return s.db.Close() }

// GetUser returns the caller's own opt-in, or errNotFound when they have never set
// one (the handler then serves the private default).
func (s *optinStore) GetUser(ctx context.Context, userID string) (userOptin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, org, handle, listed, created_at, updated_at FROM user_optin WHERE user_id=?`, userID)
	var u userOptin
	var listed int
	err := row.Scan(&u.UserID, &u.Org, &u.Handle, &listed, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return userOptin{}, errNotFound
	}
	if err != nil {
		return userOptin{}, fmt.Errorf("get user optin: %w", err)
	}
	u.Listed = listed != 0
	return u, nil
}

// PutUser upserts the caller's own opt-in on the user_id key. The org is pinned to
// the caller's validated org at the call site.
func (s *optinStore) PutUser(ctx context.Context, u userOptin, now int64) error {
	listed := 0
	if u.Listed {
		listed = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_optin (user_id, org, handle, listed, created_at, updated_at)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(user_id) DO UPDATE SET org=excluded.org, handle=excluded.handle,
		   listed=excluded.listed, updated_at=excluded.updated_at`,
		u.UserID, u.Org, u.Handle, listed, now, now)
	if err != nil {
		return fmt.Errorf("put user optin: %w", err)
	}
	return nil
}

// ListedHandles returns org's opted-in users → chosen handle (the naming source for
// a user board). Org-scoped read; only listed=1 rows.
func (s *optinStore) ListedHandles(ctx context.Context, org string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, handle FROM user_optin WHERE org=? AND listed=1`, org)
	if err != nil {
		return nil, fmt.Errorf("list handles: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, handle string
		if err := rows.Scan(&id, &handle); err != nil {
			return nil, err
		}
		out[id] = handle
	}
	return out, rows.Err()
}

// GetOrg returns an org's opt-in, or errNotFound.
func (s *optinStore) GetOrg(ctx context.Context, org string) (orgOptin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org, display, listed, created_at, updated_at FROM org_optin WHERE org=?`, org)
	var o orgOptin
	var listed int
	err := row.Scan(&o.Org, &o.Display, &listed, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return orgOptin{}, errNotFound
	}
	if err != nil {
		return orgOptin{}, fmt.Errorf("get org optin: %w", err)
	}
	o.Listed = listed != 0
	return o, nil
}

// PutOrg upserts an org's opt-in on the org key.
func (s *optinStore) PutOrg(ctx context.Context, o orgOptin, now int64) error {
	listed := 0
	if o.Listed {
		listed = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_optin (org, display, listed, created_at, updated_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(org) DO UPDATE SET display=excluded.display,
		   listed=excluded.listed, updated_at=excluded.updated_at`,
		o.Org, o.Display, listed, now, now)
	if err != nil {
		return fmt.Errorf("put org optin: %w", err)
	}
	return nil
}

// ListedOrgs returns all opted-in orgs → display (the visibility set + naming for a
// non-super global board).
func (s *optinStore) ListedOrgs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org, display FROM org_optin WHERE listed=1`)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var org, display string
		if err := rows.Scan(&org, &display); err != nil {
			return nil, err
		}
		out[org] = display
	}
	return out, rows.Err()
}
