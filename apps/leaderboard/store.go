// The opt-in preference store: the durable, OPT-IN-BY-DEFAULT-PRIVATE half of the
// leaderboard. Two tiny tenant-keyed tables in one database — a SQLite file or a
// schema on hanzo-sql (locus.Open), in the dialect both engines share
// (sdk/dialect) — with a mandatory key predicate on every statement.
//
//   - user_optin  (user_id PK, org, handle, listed): a user opts THEMSELVES into
//     public listing with a chosen handle. Default absent ⇒ NOT listed (private):
//     the user still sees their own rank, but is never named to others.
//   - org_optin   (org PK, display, listed): an org admin opts the ORG into the
//     public global board with a display name. Default absent ⇒ private.
//
// Isolation: a user row is written only for the CALLER's own user_id; the listing
// read is org-scoped (`WHERE org=$1 AND listed=1`). An org row is written only by an
// admin of that org; the global-listing read is `WHERE listed=1` (org-level display
// only — no user data). No secret ever lands here.

package leaderboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"

	// locus.Open is the ONE opener: on hanzo-sql a schema, otherwise a SQLite file
	// born encrypted under the key cek derives from the process master and the
	// system namespace, with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
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

// optinStore is the metastore over one database — the deployment's own
// "leaderboard", a SQLite file or a schema on hanzo-sql.
//
// bounty_awards.email is a SEALED column (internal/org, field.go) on both engines, bound to
// the award's id: it is the reporter's address, kept for the payout, and nothing reads it
// in a WHERE or an ORDER BY. Left plain: the handles (user_optin.handle is the name a user
// chose to be listed under, and bounty_awards.handle is the name the public board shows
// the award under), org names, points and credits, and times.
type optinStore struct {
	db     *sql.DB
	fields *tenure.Fields
}

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "leaderboard"

var awardKey, awardSealed = []string{"id"}, []string{"email"}

// bit is a flag as the integer a column holds: 1 is true, on both engines.
func bit(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// openOptinStore opens the database and the sealed column's keys. master is
// cloud.Deps.MasterKey; with none the store does not open, so no address is ever written
// in the clear because a key was missing.
func openOptinStore(dir string, master []byte) (*optinStore, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*optinStore, error) {
		s := &optinStore{db: db, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *optinStore) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		// Every integer is BIGINT: PostgreSQL's INTEGER is 32 bits, and SQLite gives
		// BIGINT the same 64-bit affinity it gives INTEGER.
		const ddl = `
CREATE TABLE IF NOT EXISTS user_optin (
  user_id    TEXT NOT NULL PRIMARY KEY,
  org        TEXT NOT NULL,
  handle     TEXT NOT NULL DEFAULT '',
  listed     BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_user_optin_org ON user_optin(org, listed);
CREATE TABLE IF NOT EXISTS org_optin (
  org        TEXT NOT NULL PRIMARY KEY,
  display    TEXT NOT NULL DEFAULT '',
  listed     BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_org_optin_listed ON org_optin(listed);
CREATE TABLE IF NOT EXISTS bounty_awards (
  id            TEXT NOT NULL PRIMARY KEY,
  org           TEXT NOT NULL,
  user_id       TEXT NOT NULL DEFAULT '',
  handle        TEXT NOT NULL DEFAULT '',
  email         TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL DEFAULT 'bug_report',
  target        TEXT NOT NULL DEFAULT '',
  summary       TEXT NOT NULL DEFAULT '',
  points        BIGINT NOT NULL DEFAULT 0,
  credits_cents BIGINT NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_bounty_awards_org ON bounty_awards(org, created_at);`
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		return s.seedDefaultBounties(ctx, tx)
	}); err != nil {
		return err
	}
	// A SQLite file from before the email was sealed holds it in plaintext. Seal it in
	// place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	if _, err := s.fields.Upgrade(context.Background(), s.db, "bounty_awards", awardKey, awardSealed); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// bountyAward is one verified bounty or bug-report award recorded on the leaderboard.
type bountyAward struct {
	ID           string `json:"id"`
	Org          string `json:"org"`
	UserID       string `json:"userId,omitempty"`
	Handle       string `json:"handle"`
	Email        string `json:"email,omitempty"`
	Kind         string `json:"kind"`
	Target       string `json:"target"`
	Summary      string `json:"summary"`
	Points       int    `json:"points"`
	CreditsCents int64  `json:"creditsCents"`
	CreatedAt    int64  `json:"createdAt"`
}

// seedDefaultBounties records the awards the board starts with, inside the migration.
// An award is an upsert on its id, so opening the store again leaves them as they were.
func (s *optinStore) seedDefaultBounties(ctx context.Context, tx *sql.Tx) error {
	const ts = 1793754000
	seeds := []bountyAward{
		{
			ID:           "bty_valerypetrov_index_lock",
			Org:          "valerypetrov",
			UserID:       "valerypetrov/valerypetrov",
			Handle:       "valerypetrov (zlatan mioslav)",
			Kind:         "bug_report",
			Target:       "hanzo-inc/cloud#index-lock",
			Summary:      "Reported post-turn git staging failure when a sandbox subprocess leaves .git/index.lock behind",
			Points:       50,
			CreditsCents: 2000,
			CreatedAt:    ts,
		},
		{
			ID:           "bty_valerypetrov_repo_fallback",
			Org:          "valerypetrov",
			UserID:       "valerypetrov/valerypetrov",
			Handle:       "valerypetrov (zlatan mioslav)",
			Kind:         "bug_report",
			Target:       "hanzo-inc/cloud#github-repo-fallback",
			Summary:      "Reported Try Again / follow-up repository resolution failure when org name matches GitHub owner (valerypetrov/ClickHouse)",
			Points:       50,
			CreditsCents: 0,
			CreatedAt:    ts + 60,
		},
		{
			ID:           "bty_valerypetrov_error_border",
			Org:          "valerypetrov",
			UserID:       "valerypetrov/valerypetrov",
			Handle:       "valerypetrov (zlatan mioslav)",
			Kind:         "bug_report",
			Target:       "hanzo-inc/app#error-card-border",
			Summary:      "Reported Error & Try Again card border contrast and live session loading state on Hanzo Dev",
			Points:       50,
			CreditsCents: 0,
			CreatedAt:    ts + 120,
		},
	}
	for _, a := range seeds {
		if err := s.putBountyAward(ctx, tx, a); err != nil {
			return err
		}
	}
	return nil
}

// PutBountyAward inserts or updates a bounty award row by id.
func (s *optinStore) PutBountyAward(ctx context.Context, a bountyAward) error {
	return s.putBountyAward(ctx, s.db, a)
}

// exec is what a handle and a transaction both run a statement on.
type exec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *optinStore) putBountyAward(ctx context.Context, db exec, a bountyAward) error {
	if err := dialect.Text(a.ID, a.Org, a.UserID, a.Handle, a.Email, a.Kind, a.Target, a.Summary); err != nil {
		return fmt.Errorf("put bounty award: %w", err)
	}
	r := s.fields.Row("bounty_awards", a.ID)
	email := r.Seal("email", a.Email)
	if err := r.Err(); err != nil {
		return fmt.Errorf("put bounty award: %w", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO bounty_awards (id, org, user_id, handle, email, kind, target, summary, points, credits_cents, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT(id) DO UPDATE SET handle=excluded.handle, summary=excluded.summary,
		   points=excluded.points, credits_cents=excluded.credits_cents`,
		a.ID, a.Org, a.UserID, a.Handle, email, a.Kind, a.Target, a.Summary, a.Points, a.CreditsCents, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("put bounty award: %w", err)
	}
	return nil
}

// ListBountyAwards returns recent bounty awards, newest first, up to limit.
func (s *optinStore) ListBountyAwards(ctx context.Context, limit int) ([]bountyAward, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, user_id, handle, kind, target, summary, points, credits_cents, created_at
		 FROM bounty_awards ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list bounty awards: %w", err)
	}
	defer rows.Close()
	var out []bountyAward
	for rows.Next() {
		var a bountyAward
		if err := rows.Scan(&a.ID, &a.Org, &a.UserID, &a.Handle, &a.Kind, &a.Target, &a.Summary, &a.Points, &a.CreditsCents, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BountyTotals sums every org's awarded points. ListBountyAwards is a capped feed;
// the standing needs the whole sum.
func (s *optinStore) BountyTotals(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org, CAST(SUM(points) AS BIGINT) FROM bounty_awards GROUP BY org`)
	if err != nil {
		return nil, fmt.Errorf("bounty totals: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var org string
		var points int
		if err := rows.Scan(&org, &points); err != nil {
			return nil, err
		}
		out[org] = points
	}
	return out, rows.Err()
}

// choice is an org's standing answer to "may the board name us": Listed with the
// Handle to show, or not Listed (an explicit opt-out).
type choice struct {
	Handle string
	Listed bool
}

// Choices reads every explicit public-listing choice, one per org. The org's own
// answer (org_optin, set by its admin) is final, listed or withdrawn. An org with
// none is answered by its users (user_optin): Listed under the handle of one that
// listed, else opted out when every user row withdrew. An org with no row at all is
// absent: it has not chosen.
func (s *optinStore) Choices(ctx context.Context) (map[string]choice, error) {
	out := map[string]choice{}
	for _, q := range []string{
		`SELECT org, display, listed FROM org_optin ORDER BY org`,
		`SELECT org, handle, listed FROM user_optin ORDER BY listed DESC, updated_at DESC, user_id`,
	} {
		decided := map[string]bool{}
		for org := range out {
			decided[org] = true
		}
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("choices: %w", err)
		}
		seen := map[string]bool{}
		for rows.Next() {
			var org, handle string
			var listed int
			if err := rows.Scan(&org, &handle, &listed); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if decided[org] || seen[org] {
				continue
			}
			seen[org] = true
			out[org] = choice{Handle: handle, Listed: listed != 0}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *optinStore) Close() error { return s.db.Close() }

// GetUser returns the caller's own opt-in, or errNotFound when they have never set
// one (the handler then serves the private default).
func (s *optinStore) GetUser(ctx context.Context, userID string) (userOptin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, org, handle, listed, created_at, updated_at FROM user_optin WHERE user_id=$1`, userID)
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
	if err := dialect.Text(u.UserID, u.Org, u.Handle); err != nil {
		return fmt.Errorf("put user optin: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_optin (user_id, org, handle, listed, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT(user_id) DO UPDATE SET org=excluded.org, handle=excluded.handle,
		   listed=excluded.listed, updated_at=excluded.updated_at`,
		u.UserID, u.Org, u.Handle, bit(u.Listed), now, now)
	if err != nil {
		return fmt.Errorf("put user optin: %w", err)
	}
	return nil
}

// ListedHandles returns org's opted-in users → chosen handle (the naming source for
// a user board). Org-scoped read; only listed=1 rows.
func (s *optinStore) ListedHandles(ctx context.Context, org string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, handle FROM user_optin WHERE org=$1 AND listed=1`, org)
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
		`SELECT org, display, listed, created_at, updated_at FROM org_optin WHERE org=$1`, org)
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
	if err := dialect.Text(o.Org, o.Display); err != nil {
		return fmt.Errorf("put org optin: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_optin (org, display, listed, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT(org) DO UPDATE SET display=excluded.display,
		   listed=excluded.listed, updated_at=excluded.updated_at`,
		o.Org, o.Display, bit(o.Listed), now, now)
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
