package team

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	// The ONE Hanzo SQLite driver (see store.go). Blank-imported here too so this
	// file's sql.Open("sqlite", …) is self-documenting about its driver dependency.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// errNoWorkspace is returned when a slug/account resolves to no workspace in the
// caller's org. Handlers map it to the platform WorkspaceNotFound status.
var errNoWorkspace = errors.New("team: workspace not found")

// accountStore is the login/membership control plane — the raw-SQLite replacement
// for team-go's Base `workspaces` + `members` collections. ONE SQLite file
// ({DataDir}/team/account.db) holds every org's rows; tenant isolation is the
// owner_org column, enforced on EVERY query (a workspace and its members are only
// ever read/selected scoped to the caller's VERIFIED token org). MaxOpenConns(1)
// serializes writes against the single-writer file.
type accountStore struct {
	db *sql.DB
}

// workspace is one team workspace. uuid is the transactor store key + token claim;
// slug is the human URL handle; ownerOrg is the tenant.
type workspace struct {
	ID        string
	Slug      string
	Name      string
	UUID      string
	Owner     string // creating account uuid (provenance only, never a tenant)
	OwnerOrg  string // IAM tenant — the isolation key
	DataID    string
	Region    string
	CreatedAt int64
}

// member is one workspace membership row (human or bot).
type member struct {
	WorkspaceID string
	UserID      string
	Role        string
	DisplayName string
	IsBot       bool
	Active      bool
	JoinedAt    int64
}

func openAccountStore(path string) (*accountStore, error) {
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
	s := &accountStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the workspaces + members tables. Every uniqueness/lookup index
// leads with owner_org so tenant isolation is a physical property. Idempotent.
func (s *accountStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS workspaces (
  id          TEXT PRIMARY KEY,
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  uuid        TEXT NOT NULL,
  owner       TEXT NOT NULL DEFAULT '',
  owner_org   TEXT NOT NULL,
  data_id     TEXT NOT NULL DEFAULT '',
  region      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_workspaces_uuid      ON workspaces(uuid);
CREATE UNIQUE INDEX IF NOT EXISTS ux_workspaces_org_slug  ON workspaces(owner_org, slug);
CREATE INDEX        IF NOT EXISTS ix_workspaces_org       ON workspaces(owner_org);

CREATE TABLE IF NOT EXISTS members (
  workspace_id TEXT NOT NULL,
  user_id      TEXT NOT NULL,
  role         TEXT NOT NULL DEFAULT 'member',
  display_name TEXT NOT NULL DEFAULT '',
  is_bot       INTEGER NOT NULL DEFAULT 0,
  active       INTEGER NOT NULL DEFAULT 1,
  joined_at    INTEGER NOT NULL,
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS ix_members_user ON members(user_id);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("account migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *accountStore) Close() error { return s.db.Close() }

const wsCols = `id,slug,name,uuid,owner,owner_org,data_id,region,created_at`

func scanWorkspace(sc interface{ Scan(...any) error }) (workspace, error) {
	var w workspace
	err := sc.Scan(&w.ID, &w.Slug, &w.Name, &w.UUID, &w.Owner, &w.OwnerOrg, &w.DataID, &w.Region, &w.CreatedAt)
	return w, err
}

// EnsureWorkspace gives an account a personal workspace in org if it has none, so
// the workspace picker is never empty. owner_org is the canonical tenant field
// every downstream surface (transactor path, roster reconcile) reads; `owner`
// keeps the creating account uuid for provenance only. Returns the account's
// (possibly pre-existing) first workspace in the org.
func (s *accountStore) EnsureWorkspace(ctx context.Context, org, account, name string) (workspace, error) {
	if org == "" || account == "" {
		return workspace{}, fmt.Errorf("team: ensure workspace: empty org/account")
	}
	existing, err := s.WorkspacesOf(ctx, org, account)
	if err != nil {
		return workspace{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	if name == "" {
		name = "Workspace"
	}
	w := workspace{
		ID:        uuid.NewString(),
		Slug:      slugify(name) + "-" + shortID(),
		Name:      name,
		UUID:      uuid.NewString(),
		Owner:     account,
		OwnerOrg:  org,
		CreatedAt: time.Now().UnixMilli(),
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspace{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces (`+wsCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Slug, w.Name, w.UUID, w.Owner, w.OwnerOrg, w.DataID, w.Region, w.CreatedAt); err != nil {
		return workspace{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
		 VALUES (?,?,?,?,?,?,?)`,
		w.ID, account, "owner", name, 0, 1, w.CreatedAt); err != nil {
		return workspace{}, fmt.Errorf("insert owner member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return workspace{}, fmt.Errorf("commit: %w", err)
	}
	return w, nil
}

// WorkspacesOf returns the org's workspaces the account is a member of, newest
// first. The join is scoped by owner_org so an account never resolves a workspace
// outside the caller's VERIFIED token org.
func (s *accountStore) WorkspacesOf(ctx context.Context, org, account string) ([]workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+prefixed("w", wsCols)+` FROM workspaces w
		 JOIN members m ON m.workspace_id = w.id
		 WHERE w.owner_org = ? AND m.user_id = ?
		 ORDER BY m.joined_at DESC, w.created_at DESC`, org, account)
	if err != nil {
		return nil, fmt.Errorf("workspaces of: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkspaceBySlug resolves a workspace by (org, slug) — the org scope is what stops
// a caller in org A from selecting org B's workspace by passing its slug. Returns
// errNoWorkspace when absent in the org.
func (s *accountStore) WorkspaceBySlug(ctx context.Context, org, slug string) (workspace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+wsCols+` FROM workspaces WHERE owner_org = ? AND slug = ?`, org, slug)
	w, err := scanWorkspace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace{}, errNoWorkspace
	}
	if err != nil {
		return workspace{}, fmt.Errorf("workspace by slug: %w", err)
	}
	return w, nil
}

// WorkspaceByUUID resolves a workspace by (org, uuid) — the org scope is the
// tenant boundary the files plane relies on: a blob request naming another org's
// workspace uuid resolves to errNoWorkspace, so it can never reach that org's
// blobs. Returns errNoWorkspace when absent in the org.
func (s *accountStore) WorkspaceByUUID(ctx context.Context, org, uuid string) (workspace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+wsCols+` FROM workspaces WHERE owner_org = ? AND uuid = ?`, org, uuid)
	w, err := scanWorkspace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace{}, errNoWorkspace
	}
	if err != nil {
		return workspace{}, fmt.Errorf("workspace by uuid: %w", err)
	}
	return w, nil
}

// Membership returns the account's role in a workspace, or ("", false) if the
// account is not a member. The role is ALWAYS read from the members row, never a
// self-asserted claim.
func (s *accountStore) Membership(ctx context.Context, workspaceID, account string) (string, bool) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM members WHERE workspace_id = ? AND user_id = ?`, workspaceID, account).Scan(&role)
	if err != nil {
		return "", false
	}
	return role, true
}

// MembersForWorkspaceUUID returns the human member rows of a workspace, resolved
// by (org, workspace uuid) so a foreign tenant's uuid returns nothing. This is the
// human half of the roster reconcile.
func (s *accountStore) MembersForWorkspaceUUID(ctx context.Context, org, wsUUID string) ([]member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.workspace_id,m.user_id,m.role,m.display_name,m.is_bot,m.active,m.joined_at
		 FROM members m JOIN workspaces w ON w.id = m.workspace_id
		 WHERE w.owner_org = ? AND w.uuid = ?`, org, wsUUID)
	if err != nil {
		return nil, fmt.Errorf("members for workspace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []member
	for rows.Next() {
		var m member
		var isBot, active int
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Role, &m.DisplayName, &isBot, &active, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.IsBot = isBot != 0
		m.Active = active != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// GuestRank returns the 1-based join-order rank of account among the
// workspace's guest-role members, 0 when the account is not a guest there. The
// entitle gate compares it to the plan's team.guests cap so the FIRST cap
// guests keep access deterministically (joined_at, user_id tie-break) and later
// invites are refused — never all-or-nothing.
func (s *accountStore) GuestRank(ctx context.Context, workspaceID, account string) int {
	var rank int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM members g, members me
		  WHERE me.workspace_id = ? AND me.user_id = ? AND me.role = ?
		    AND g.workspace_id = me.workspace_id AND g.role = me.role
		    AND (g.joined_at < me.joined_at OR (g.joined_at = me.joined_at AND g.user_id <= me.user_id))`,
		workspaceID, account, roleGuest).Scan(&rank)
	if err != nil {
		return 0
	}
	return rank
}

// Seats counts the org's distinct ACTIVE human members across all of its
// workspaces (the billed seats), and how many of those are invited guests.
// Bots never occupy a seat. Backs GET /v1/team/billing/plan.
func (s *accountStore) Seats(ctx context.Context, org string) (seats, guests int) {
	const q = `SELECT
	  COUNT(DISTINCT m.user_id),
	  COUNT(DISTINCT CASE WHEN m.role = ? THEN m.user_id END)
	FROM members m JOIN workspaces w ON w.id = m.workspace_id
	WHERE w.owner_org = ? AND m.active = 1 AND m.is_bot = 0`
	_ = s.db.QueryRowContext(ctx, q, roleGuest, org).Scan(&seats, &guests)
	return seats, guests
}

// EnsureMemberName fills display_name on the account's member rows when empty, so
// the projected Person shows a human name rather than the account uuid. Idempotent
// (only fills empty). Scoped to the account's own rows.
func (s *accountStore) EnsureMemberName(ctx context.Context, account, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE members SET display_name = ? WHERE user_id = ? AND display_name = ''`, name, account)
	return err
}

// WorkspacesForOrg returns every workspace of an org — used by /v1/team/bots/sync
// to re-project the org's agents into each of its workspaces. Org-scoped.
func (s *accountStore) WorkspacesForOrg(ctx context.Context, org string) ([]workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+wsCols+` FROM workspaces WHERE owner_org = ? ORDER BY created_at DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("workspaces for org: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// prefixed rewrites a comma-column list to a table-qualified one (w.id,w.slug,…)
// for the membership join. cols is a package-internal literal, never user input.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + c
	}
	return strings.Join(parts, ",")
}
