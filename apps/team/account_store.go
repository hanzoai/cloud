package team

import (
	"context"
	"errors"
	"fmt"
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

-- Personal-workspace uniqueness. EnsureWorkspace is the SOLE creator of workspaces
-- and always writes owner=account, so (owner_org, owner) identifies an account's ONE
-- personal workspace. A pre-fix login race could mint duplicates (two concurrent
-- logins both saw "none"); converge any such duplicates to the EARLIEST row (drop the
-- extras' member rows, then the extras) before enforcing the invariant going forward.
-- The index is PARTIAL (owner <> '') so any owner-less row — none created here — stays
-- unconstrained rather than colliding. Idempotent: no-op once converged.
DELETE FROM members WHERE workspace_id IN (
  SELECT w.id FROM workspaces w
  WHERE w.owner <> '' AND EXISTS (
    SELECT 1 FROM workspaces e
    WHERE e.owner_org = w.owner_org AND e.owner = w.owner AND e.id <> w.id
      AND (e.created_at < w.created_at OR (e.created_at = w.created_at AND e.id < w.id))
  )
);
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
	if _, err := tx.Insert("members", query.Params{
		"workspace_id": w.ID,
		"user_id":      account,
		"role":         "owner",
		"display_name": name,
		"is_bot":       0,
		"active":       1,
		"joined_at":    w.CreatedAt,
	}).WithContext(ctx).Execute(); err != nil {
		return workspace{}, fmt.Errorf("insert owner member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return workspace{}, fmt.Errorf("commit: %w", err)
	}
	return w, nil
}

// adoptExisting returns the account's first workspace in the org (newest membership
// first), having HEALED the caller's own member row to an active human seat. ok is
// false when the account has no workspace yet. Used both on the fast path and after
// losing the personal-workspace create race (adopt the concurrent winner).
//
// Heal rationale: a user who just authenticated through IAM is, by definition, an
// ACTIVE, non-bot member of their workspace — but a row migrated from team-go (or
// otherwise) can carry is_bot=1 / active=0, which excludes the owner from the billed
// seat count (Seats filters active=1 AND is_bot=0) even though WorkspacesOf still
// lists the workspace (it does not filter those flags). Force the caller's OWN row —
// never anyone else's — to an active human seat. Idempotent: a correct row is left
// unchanged.
func (s *accountStore) adoptExisting(ctx context.Context, org, account string) (workspace, bool, error) {
	existing, err := s.WorkspacesOf(ctx, org, account)
	if err != nil {
		return workspace{}, false, err
	}
	if len(existing) == 0 {
		return workspace{}, false, nil
	}
	if _, err := s.db.Update("members",
		query.Params{"active": 1, "is_bot": 0},
		query.HashExp{"workspace_id": existing[0].ID, "user_id": account},
	).WithContext(ctx).Execute(); err != nil {
		return workspace{}, false, fmt.Errorf("heal member: %w", err)
	}
	return existing[0], true, nil
}

// WorkspacesOf returns the org's workspaces the account is a member of, newest
// first. The join is scoped by owner_org so an account never resolves a workspace
// outside the caller's VERIFIED org.
func (s *accountStore) WorkspacesOf(ctx context.Context, org, account string) ([]workspace, error) {
	t := s.workspacesIn("workspaces w", prefixed("w", wsCols)...)
	t.Query().InnerJoin("members m", query.NewExp("m.workspace_id = w.id"))
	out, err := t.
		Where(query.HashExp{"w.owner_org": org, "m.user_id": account}).
		OrderBy("m.joined_at DESC", "w.created_at DESC").
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

// Membership returns the account's role in a workspace, or ("", false) if the
// account is not a member. The role is ALWAYS read from the members row, never a
// self-asserted claim.
func (s *accountStore) Membership(ctx context.Context, workspaceID, account string) (string, bool) {
	var role string
	err := s.db.Select("role").From("members").
		Where(query.HashExp{"workspace_id": workspaceID, "user_id": account}).
		WithContext(ctx).Row(&role)
	if err != nil {
		return "", false
	}
	return role, true
}

// MemberName returns the display name on a member row, empty when the row has
// none or does not exist. It is deliberately NOT folded into Membership: a role
// is what a member may DO and is read by every gate, while a display name is
// decoration read by the few surfaces that render a person. Empty is a real
// answer — EnsureMemberName only fills a name that a login supplied.
func (s *accountStore) MemberName(ctx context.Context, workspaceID, account string) string {
	var name string
	err := s.db.Select("display_name").From("members").
		Where(query.HashExp{"workspace_id": workspaceID, "user_id": account}).
		WithContext(ctx).Row(&name)
	if err != nil {
		return ""
	}
	return name
}

// AccountForSubject is the ONE answer to "which team account is this IAM
// identity?", and the store is deliberately the one that gives it.
//
// The subject is the `sub` claim VERBATIM and nothing else. It is NOT the
// canonical user id: that one falls back sub → preferred_username → name, so a
// token carrying no sub presents its USERNAME there — and accountID returns a
// UUID-shaped input verbatim, so a username that is a colleague's account uuid
// would have resolved to the colleague. Subject-only closes that, and an empty
// subject is refused exactly as the OAuth callback's userinfo() refuses one.
//
// It then CONFIRMS the derived id against the rows instead of asserting it. The
// derivation (accountID) is the same function establishSession stores the rows
// under — one derivation, not two — but a login is what CREATES those rows, so an
// id that matches none is an identity this deployment has never seen, and the
// honest answer is "no account" rather than an account-shaped string every later
// query would then scope by. That is what makes the caller's refusal true rather
// than merely documented.
//
// The existence check is org-scoped: a member row is only this org's if its
// workspace is. So a subject known in org A resolves to nothing in org B, and the
// answer cannot be used to probe another tenant.
func (s *accountStore) AccountForSubject(ctx context.Context, org, subject string) (string, bool) {
	account := accountID(strings.TrimSpace(subject))
	if account == "" || strings.TrimSpace(org) == "" {
		return "", false
	}
	var found string
	err := s.db.Select("m.user_id").From("members m").
		InnerJoin("workspaces w", query.NewExp("w.id = m.workspace_id")).
		Where(query.HashExp{"w.owner_org": org, "m.user_id": account}).
		Limit(1).WithContext(ctx).Row(&found)
	if err != nil || found == "" {
		return "", false
	}
	return found, true
}

// MembersForWorkspaceUUID returns the member rows of a workspace, resolved by
// (org, workspace uuid) so a foreign tenant's uuid returns nothing. This is the
// human half of the roster reconcile.
func (s *accountStore) MembersForWorkspaceUUID(ctx context.Context, org, wsUUID string) ([]member, error) {
	t := orm.Select[member](s.db, "members m",
		"m.workspace_id", "m.user_id", "m.role", "m.display_name", "m.is_bot", "m.active", "m.joined_at")
	t.Query().InnerJoin("workspaces w", query.NewExp("w.id = m.workspace_id"))
	out, err := t.Where(query.HashExp{"w.owner_org": org, "w.uuid": wsUUID}).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("members for workspace: %w", err)
	}
	return out, nil
}

// GuestRank returns the 1-based join-order rank of account among the
// workspace's guest-role members, 0 when the account is not a guest there. The
// entitle gate compares it to the plan's team.guests cap so the FIRST cap
// guests keep access deterministically (joined_at, user_id tie-break) and later
// invites are refused — never all-or-nothing.
func (s *accountStore) GuestRank(ctx context.Context, workspaceID, account string) int {
	var rank int
	err := s.db.Select("COUNT(*)").From("members g", "members me").
		Where(query.NewExp(
			`me.workspace_id = {:ws} AND me.user_id = {:acct} AND me.role = {:role}
			 AND g.workspace_id = me.workspace_id AND g.role = me.role
			 AND (g.joined_at < me.joined_at OR (g.joined_at = me.joined_at AND g.user_id <= me.user_id))`,
			query.Params{"ws": workspaceID, "acct": account, "role": roleGuest})).
		WithContext(ctx).Row(&rank)
	if err != nil {
		return 0
	}
	return rank
}

// Seats counts the org's distinct ACTIVE human members across all of its
// workspaces (the billed seats), and how many of those are invited guests.
// Bots never occupy a seat. Backs GET /v1/team/billing/plan.
//
// The read error is PROPAGATED, never swallowed: this is an aggregate COUNT
// query that always returns exactly one row (zeros for an org with no members),
// so an error is a real DB failure — surfacing it lets the wallet's seat
// read fail honestly and retry, instead of a broken read masquerading as a
// truthful "0 members" and under-reporting the billed seat count.
func (s *accountStore) Seats(ctx context.Context, org string) (seats, guests int, err error) {
	// The guest role sits in the PROJECTION, which no Where can carry, so it binds on
	// the query itself. AndBind, never Bind: Bind REPLACES the parameter set.
	q := s.db.Select(
		"COUNT(DISTINCT m.user_id)",
		"COUNT(DISTINCT CASE WHEN m.role = {:role} THEN m.user_id END)").
		From("members m").
		InnerJoin("workspaces w", query.NewExp("w.id = m.workspace_id")).
		Where(query.NewExp("w.owner_org = {:org} AND m.active = 1 AND m.is_bot = 0",
			query.Params{"org": org})).
		AndBind(query.Params{"role": roleGuest})
	if err := q.WithContext(ctx).Row(&seats, &guests); err != nil {
		return 0, 0, fmt.Errorf("seats: %w", err)
	}
	return seats, guests, nil
}

// EnsureMemberName fills display_name on the account's member rows when empty, so
// the projected Person shows a human name rather than the account uuid. Idempotent
// (only fills empty). Scoped to the account's own rows.
func (s *accountStore) EnsureMemberName(ctx context.Context, account, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	_, err := s.db.Update("members",
		query.Params{"display_name": name},
		query.HashExp{"user_id": account, "display_name": ""},
	).WithContext(ctx).Execute()
	return err
}

// AddMember records (or re-activates) a human membership row for an invited
// account in a workspace. Idempotent: a re-invite updates the role and clears any
// prior deactivation rather than duplicating (the PK is (workspace_id, user_id)).
// The role is written verbatim (owner | admin | member | guest) — the invite path
// validates it before calling. joined_at is preserved on an existing row so guest
// join-order (GuestRank) stays deterministic across re-invites.
//
// RAW, because PRESERVING a column is the thing dbx's Upsert cannot say: it fans
// every inserted column into the DO UPDATE SET list, so joined_at and is_bot would be
// overwritten on re-invite — and overwriting joined_at is exactly the guest join-order
// GuestRank and the seat cap depend on.
func (s *accountStore) AddMember(ctx context.Context, workspaceID, account, role, displayName string) error {
	if workspaceID == "" || account == "" {
		return fmt.Errorf("team: add member: empty workspace/account")
	}
	if role == "" {
		role = "member"
	}
	_, err := s.db.NewQuery(
		`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
		 VALUES ({:ws},{:acct},{:role},{:name},0,1,{:joined})
		 ON CONFLICT(workspace_id,user_id) DO UPDATE SET
		   role=excluded.role,
		   active=1,
		   display_name=CASE WHEN members.display_name='' THEN excluded.display_name ELSE members.display_name END`).
		Bind(query.Params{
			"ws": workspaceID, "acct": account, "role": role,
			"name": displayName, "joined": time.Now().UnixMilli(),
		}).WithContext(ctx).Execute()
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
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
