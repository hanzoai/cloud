package team

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/plane/iam"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/query"

	// The ONE Hanzo SQLite driver (see store.go). Blank-imported here too because
	// query.NewFromDB below names "sqlite" as the dialect, and that name is the
	// registered driver — the dialect lookup and the driver registration are one
	// string.
	_ "github.com/hanzoai/sqlite"
)

// errNoWorkspace is returned when a slug/account resolves to no workspace in the
// caller's org. Handlers map it to the platform WorkspaceNotFound status.
var errNoWorkspace = errors.New("team: workspace not found")

// accountStore is the login/membership control plane — the workspaces + members
// tables behind team's picker, its seat count and its roster. ONE SQLite file — the
// deployment's own "account" subsystem — holds every org's rows; tenant isolation is
// the owner_org column, enforced on EVERY query (a workspace and its members are only
// ever read/selected scoped to the caller's VERIFIED org). MaxOpenConns(1) serializes
// writes against the single-writer file.
//
// The data path is hanzoai/orm's RELATIONAL plane: orm.Select/orm.Typed for the reads,
// the dbx builder (orm/query) for the writes. Not orm.Model — that is the document
// plane over the JSON `_entities` table, and these tables are typed and indexed, with
// composite uniqueness ((owner_org, slug), (workspace_id, user_id)) that a
// json_extract store cannot hold. Three statements resist the builder and say so where
// they stand; they run through orm's own NewQuery, so this file has ONE data path and
// no database/sql handle of its own.
//
// The handle comes from cek — an ENCRYPTED SQLite file, keyed per (namespace,
// subsystem) — and orm is adapted onto it rather than opening its own. orm's
// SQLiteDBConfig carries no master key, so orm.OpenSQLite would write this store —
// every workspace, membership and display name — as a plaintext `SQLite format 3`
// file. Adapting a handle the caller opened is what keeps encryption at rest.
type accountStore struct {
	db *query.DB
}

// workspace is one team workspace. uuid is the transactor store key + token claim;
// slug is the human URL handle; ownerOrg is the tenant.
//
// The `db` tags are the mapping dbx scans by, which is why the hand-written positional
// Scan is gone: a column added to wsCols without a matching field is now a mapping
// that does not resolve, rather than a silent shift of every value one position left.
type workspace struct {
	ID        string `db:"id"`
	Slug      string `db:"slug"`
	Name      string `db:"name"`
	UUID      string `db:"uuid"`
	Owner     string `db:"owner"`     // creating account uuid (provenance only, never a tenant)
	OwnerOrg  string `db:"owner_org"` // IAM tenant — the isolation key
	DataID    string `db:"data_id"`
	Region    string `db:"region"`
	CreatedAt int64  `db:"created_at"`
}

// member is one workspace membership row (human or bot).
type member struct {
	WorkspaceID string `db:"workspace_id"`
	UserID      string `db:"user_id"`
	Role        string `db:"role"`
	DisplayName string `db:"display_name"`
	IsBot       bool   `db:"is_bot"`
	Active      bool   `db:"active"`
	JoinedAt    int64  `db:"joined_at"`
}

func openAccountStore(dir string) (*accountStore, error) {
	conn, err := cek.Open(namespace.System(), "account", dir)
	if err != nil {
		return nil, fmt.Errorf("open account store: %w", err)
	}
	sqlpool.Single(conn)
	// "sqlite" is load-bearing twice over: it is the name hanzoai/sqlite registers the
	// driver under AND the key dbx maps to its SQLite builder. An unrecognised name
	// falls back to the standard builder, whose INSERT cannot say ON CONFLICT at all.
	s := &accountStore{db: query.NewFromDB(conn, "sqlite")}
	if err := s.migrate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the workspaces + members tables. Every uniqueness/lookup index
// leads with owner_org so tenant isolation is a physical property. Idempotent.
//
// RAW, and it has to be: the builder's CreateUniqueIndex emits neither IF NOT EXISTS
// nor a WHERE, so it can express neither the idempotence this needs nor the PARTIAL
// index below, and dbx.Sync writes no indexes at all. It runs through orm's NewQuery,
// so the escape hatch stays inside the one data path.
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

-- Personal-workspace uniqueness. EnsureWorkspace is the SOLE creator of workspaces
-- and always writes owner=account, so (owner_org, owner) identifies an account's ONE
-- personal workspace. A pre-fix login race could mint duplicates (two concurrent
-- logins both saw "none"); converge any such duplicates to the EARLIEST row before
-- enforcing the invariant going forward. The extras' grants need no sweep: the
-- backfill joins members to workspaces, so a grant whose workspace is gone is
-- never carried into IAM.
-- The index is PARTIAL (owner <> '') so any owner-less row — none created here — stays
-- unconstrained rather than colliding. Idempotent: no-op once converged.
DELETE FROM workspaces WHERE owner <> '' AND id IN (
  SELECT w.id FROM workspaces w
  WHERE EXISTS (
    SELECT 1 FROM workspaces e
    WHERE e.owner_org = w.owner_org AND e.owner = w.owner AND e.id <> w.id
      AND (e.created_at < w.created_at OR (e.created_at = w.created_at AND e.id < w.id))
  )
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_workspaces_org_owner ON workspaces(owner_org, owner) WHERE owner <> '';
`
	if _, err := s.db.NewQuery(ddl).Execute(); err != nil {
		return fmt.Errorf("account migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. dbx.DB.Close closes the *sql.DB cek handed
// us, so the handle is owned in exactly one place.
func (s *accountStore) Close() error { return s.db.Close() }

// wsCols is the workspace projection — the columns every workspace read selects and
// the order EnsureWorkspace inserts by.
var wsCols = []string{"id", "slug", "name", "uuid", "owner", "owner_org", "data_id", "region", "created_at"}

// workspacesIn starts a workspace read over a table expression. ONE spelling of
// "select a workspace", so a projection can never disagree with the struct.
func (s *accountStore) workspacesIn(table string, cols ...string) *orm.Typed[workspace] {
	if len(cols) == 0 {
		cols = wsCols
	}
	return orm.Select[workspace](s.db, table, cols...)
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
	// Fast path: the account already has a workspace → heal own membership, return it.
	if w, ok, err := s.adoptExisting(ctx, org, account); err != nil {
		return workspace{}, err
	} else if ok {
		return w, nil
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
	// Idempotent create: the (owner_org, owner) partial-unique index makes a racing
	// second login's INSERT a no-op (RowsAffected 0). The winner also writes the owner
	// member row and commits; a loser rolls back its unused ids and ADOPTS the winner,
	// so concurrent logins converge to exactly ONE personal workspace.
	//
	// RAW, on two counts the builder has no client for: DO NOTHING (dbx's Upsert only
	// ever emits DO UPDATE SET) and a conflict target carrying the partial index's
	// WHERE. Written as a DO UPDATE it would invert the meaning — the loser would
	// overwrite the winner's row instead of yielding to it.
	res, err := tx.NewQuery(
		`INSERT INTO workspaces (` + strings.Join(wsCols, ",") + `)
		 VALUES ({:id},{:slug},{:name},{:uuid},{:owner},{:owner_org},{:data_id},{:region},{:created_at})
		 ON CONFLICT(owner_org, owner) WHERE owner <> '' DO NOTHING`).
		Bind(query.Params{
			"id": w.ID, "slug": w.Slug, "name": w.Name, "uuid": w.UUID,
			"owner": w.Owner, "owner_org": w.OwnerOrg, "data_id": w.DataID,
			"region": w.Region, "created_at": w.CreatedAt,
		}).WithContext(ctx).Execute()
	if err != nil {
		return workspace{}, fmt.Errorf("insert workspace: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		if w, ok, err := s.adoptExisting(ctx, org, account); err != nil {
			return workspace{}, err
		} else if ok {
			return w, nil
		}
		return workspace{}, fmt.Errorf("team: ensure workspace: create conflicted but no personal workspace resolved")
	}
	if err := tx.Commit(); err != nil {
		return workspace{}, fmt.Errorf("commit: %w", err)
	}
	// The creator owns it, recorded where every other grant lives. AFTER the
	// commit, because the workspace has to exist before a grant can name it: the
	// grant is idempotent, so a failure here leaves a workspace its creator
	// re-owns on the next call rather than a row pointing at nothing.
	if err := s.AddMember(ctx, org, w.UUID, account, "owner"); err != nil {
		return workspace{}, fmt.Errorf("grant owner: %w", err)
	}
	return w, nil
}

// adoptExisting returns the account's first workspace in the org, if any.
//
// It heals nothing: the grant it used to repair is IAM's row now, and a workspace
// this account can be found through is one IAM already says they may act in.
func (s *accountStore) adoptExisting(ctx context.Context, org, account string) (workspace, bool, error) {
	existing, err := s.WorkspacesOf(ctx, org, account)
	if err != nil {
		return workspace{}, false, err
	}
	if len(existing) == 0 {
		return workspace{}, false, nil
	}
	return existing[0], true, nil
}

// WorkspacesOf returns the org's workspaces the account may act in, newest first.
//
// Where a person may act is IAM's answer; which workspace carries which name is
// team's. So this asks IAM for the scopes and reads the rows for them, rather
// than joining a roster it no longer keeps.
func (s *accountStore) WorkspacesOf(ctx context.Context, org, account string) ([]workspace, error) {
	got, err := iam.IAMMembers(cloud.For(ctx, org), &plane.Scope{User: account, Any: true})
	if err != nil {
		return nil, fmt.Errorf("workspaces of: %w", err)
	}
	uuids := make([]any, 0, len(got.Memberships))
	for _, m := range got.Memberships {
		if m.Workspace != "" {
			uuids = append(uuids, m.Workspace)
		}
	}
	if len(uuids) == 0 {
		return nil, nil
	}
	out, err := s.workspacesIn("workspaces w", prefixed("w", wsCols)...).
		Where(query.HashExp{"w.owner_org": org, "w.uuid": uuids}).
		OrderBy("w.created_at DESC").
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspaces of: %w", err)
	}
	return out, nil
}

// WorkspaceBySlug resolves a workspace by (org, slug) — the org scope is what stops
// a caller in org A from selecting org B's workspace by passing its slug. Returns
// errNoWorkspace when absent in the org.
func (s *accountStore) WorkspaceBySlug(ctx context.Context, org, slug string) (workspace, error) {
	w, ok, err := s.workspacesIn("workspaces").
		Where(query.HashExp{"owner_org": org, "slug": slug}).First(ctx)
	if err != nil {
		return workspace{}, fmt.Errorf("workspace by slug: %w", err)
	}
	if !ok {
		return workspace{}, errNoWorkspace
	}
	return w, nil
}

// WorkspaceByUUID resolves a workspace by (org, uuid) — the org scope is the
// tenant boundary the files plane relies on: a blob request naming another org's
// workspace uuid resolves to errNoWorkspace, so it can never reach that org's
// blobs. Returns errNoWorkspace when absent in the org.
func (s *accountStore) WorkspaceByUUID(ctx context.Context, org, id string) (workspace, error) {
	w, ok, err := s.workspacesIn("workspaces").
		Where(query.HashExp{"owner_org": org, "uuid": id}).First(ctx)
	if err != nil {
		return workspace{}, fmt.Errorf("workspace by uuid: %w", err)
	}
	if !ok {
		return workspace{}, errNoWorkspace
	}
	return w, nil
}

// Membership is the role a person holds in a workspace, from IAM.
//
// Team keeps no roster: identity, membership and roles are IAM's. The scope is
// the workspace UUID — team's stable external name for it, which is what the
// grant is filed against.
func (s *accountStore) Membership(ctx context.Context, org, wsUUID, account string) (string, bool) {
	for _, m := range s.roster(ctx, org, wsUUID) {
		if m.User == account {
			return m.Role, true
		}
	}
	return "", false
}

// MemberName is the name IAM holds for a member of a workspace.
func (s *accountStore) MemberName(ctx context.Context, org, wsUUID, account string) string {
	for _, m := range s.roster(ctx, org, wsUUID) {
		if m.User == account {
			return m.Name
		}
	}
	return ""
}

// AccountForSubject resolves an IAM subject to the account id team attributes work
// by, and reports whether that account may act in the org.
//
// The account id is DERIVED from the subject — team owns that join — while whether
// they may act is IAM's. So the derivation stays here and the membership question
// goes to IAM, which is the split that removed the roster.
func (s *accountStore) AccountForSubject(ctx context.Context, org, subject string) (string, bool) {
	account := accountID(strings.TrimSpace(subject))
	if account == "" || strings.TrimSpace(org) == "" {
		return "", false
	}
	got, err := iam.IAMMembers(cloud.For(ctx, org), &plane.Scope{User: account, Any: true})
	if err != nil || len(got.Memberships) == 0 {
		return "", false
	}
	return account, true
}

// MembersForWorkspaceUUID is the workspace's roster, from IAM.
func (s *accountStore) MembersForWorkspaceUUID(ctx context.Context, org, wsUUID string) ([]member, error) {
	rows := s.roster(ctx, org, wsUUID)
	out := make([]member, 0, len(rows))
	for _, m := range rows {
		out = append(out, member{
			WorkspaceID: wsUUID, UserID: m.User, Role: m.Role,
			DisplayName: m.Name, Active: true,
		})
	}
	return out, nil
}

// roster reads one workspace's memberships. It answers empty on a failure: every
// caller is deciding whether ONE person may act, and an empty roster denies.
func (s *accountStore) roster(ctx context.Context, org, wsUUID string) []plane.Membership {
	got, err := iam.IAMMembers(cloud.For(ctx, org), &plane.Scope{Workspace: wsUUID})
	if err != nil {
		return nil
	}
	return got.Memberships
}

// GuestRank is a guest's position among the workspace's guests, oldest first.
// IAM returns the roster in a stable order, so the rank is the index.
func (s *accountStore) GuestRank(ctx context.Context, org, wsUUID, account string) int {
	rank := 0
	for _, m := range s.roster(ctx, org, wsUUID) {
		if m.Role != roleGuest {
			continue
		}
		rank++
		if m.User == account {
			return rank
		}
	}
	return 0
}

// Seats is what the org is billed for, counted by IAM.
//
// The error is PROPAGATED, never swallowed: a broken read masquerading as a
// truthful "0 members" under-reports the billed seat count.
func (s *accountStore) Seats(ctx context.Context, org string) (seats, guests int, err error) {
	got, err := iam.IAMSeats(cloud.For(ctx, org))
	if err != nil {
		return 0, 0, fmt.Errorf("seats: %w", err)
	}
	return got.Seats, got.Guests, nil
}

// EnsureMemberName is gone: a person's name is IAM's, read with the roster.

// AddMember records in IAM that account may act in the workspace.
func (s *accountStore) AddMember(ctx context.Context, org, wsUUID, account, role string) error {
	if wsUUID == "" || account == "" {
		return fmt.Errorf("team: add member: empty workspace/account")
	}
	if role == "" {
		role = "member"
	}
	_, err := iam.IAMGrant(cloud.For(ctx, org), &plane.GrantIn{
		User: account, Workspace: wsUUID, Role: role,
	})
	return err
}

// WorkspacesForOrg returns every workspace of an org — used by /v1/team/bots/sync
// to re-project the org's agents into each of its workspaces. Org-scoped.
func (s *accountStore) WorkspacesForOrg(ctx context.Context, org string) ([]workspace, error) {
	out, err := s.workspacesIn("workspaces").
		Where(query.HashExp{"owner_org": org}).
		OrderBy("created_at DESC").All(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspaces for org: %w", err)
	}
	return out, nil
}

// prefixed rewrites a column list to a table-qualified one (w.id, w.slug, …) for
// the membership join. cols is a package-internal literal, never user input.
func prefixed(alias string, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = alias + "." + c
	}
	return out
}
