package team

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/iam"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/query"

	// The ONE Hanzo SQLite driver (see store.go). Blank-imported here too because
	// query.NewFromDB below names "sqlite" as the dialect, and that name is the
	// registered driver — the dialect lookup and the driver registration are one
	// string.
	_ "github.com/hanzoai/sqlite"
)

// errNoSpace is returned when a slug/account resolves to no space in the
// caller's org. Handlers map it to the platform SpaceNotFound status.
var errNoSpace = errors.New("team: space not found")

// accountStore is the login/membership control plane — the spaces + members
// tables behind team's picker, its seat count and its roster. ONE SQLite file — the
// deployment's own "account" subsystem — holds every org's rows; tenant isolation is
// the owner_org column, enforced on EVERY query (a space and its members are only
// ever read/selected scoped to the caller's VERIFIED org). MaxOpenConns(1) serializes
// writes against the single-writer file.
//
// The data path is hanzoai/orm's RELATIONAL plane: orm.Select/orm.Typed for the reads,
// the dbx builder (orm/query) for the writes. Not orm.Model — that is the document
// plane over the JSON `_entities` table, and these tables are typed and indexed, with
// composite uniqueness ((owner_org, slug), (space_id, user_id)) that a
// json_extract store cannot hold. Three statements resist the builder and say so where
// they stand; they run through orm's own NewQuery, so this file has ONE data path and
// no database/sql handle of its own.
//
// The handle comes from cek — an ENCRYPTED SQLite file, keyed per (namespace,
// subsystem) — and orm is adapted onto it rather than opening its own. orm's
// SQLiteDBConfig carries no master key, so orm.OpenSQLite would write this store —
// every space, membership and display name — as a plaintext `SQLite format 3`
// file. Adapting a handle the caller opened is what keeps encryption at rest.
type accountStore struct {
	db *query.DB
}

// space is one team space. uuid is the transactor store key + token claim;
// slug is the human URL handle; ownerOrg is the tenant.
//
// The `db` tags are the mapping dbx scans by, which is why the hand-written positional
// Scan is gone: a column added to wsCols without a matching field is now a mapping
// that does not resolve, rather than a silent shift of every value one position left.
type space struct {
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

// member is one space membership row (human or bot).
type member struct {
	SpaceID     string `db:"space_id"`
	UserID      string `db:"user_id"`
	Role        string `db:"role"`
	DisplayName string `db:"display_name"`
	IsBot       bool   `db:"is_bot"`
	Active      bool   `db:"active"`
	JoinedAt    int64  `db:"joined_at"`
}

func openAccountStore(dir string) (*accountStore, error) {
	conn, err := sqlpool.Open("account", dir)
	if err != nil {
		return nil, err
	}
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

// migrate creates the spaces + members tables. Every uniqueness/lookup index
// leads with owner_org so tenant isolation is a physical property. Idempotent.
//
// RAW, and it has to be: the builder's CreateUniqueIndex emits neither IF NOT EXISTS
// nor a WHERE, so it can express neither the idempotence this needs nor the PARTIAL
// index below, and dbx.Sync writes no indexes at all. It runs through orm's NewQuery,
// so the escape hatch stays inside the one data path.
func (s *accountStore) migrate() error {
	if err := s.converge(); err != nil {
		return err
	}
	const ddl = `
CREATE TABLE IF NOT EXISTS spaces (
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_spaces_uuid      ON spaces(uuid);
CREATE UNIQUE INDEX IF NOT EXISTS ux_spaces_org_slug  ON spaces(owner_org, slug);
CREATE INDEX        IF NOT EXISTS ix_spaces_org       ON spaces(owner_org);

-- Personal-space uniqueness. EnsureSpace is the SOLE creator of spaces
-- and always writes owner=account, so (owner_org, owner) identifies an account's ONE
-- personal space. A pre-fix login race could mint duplicates (two concurrent
-- logins both saw "none"); converge any such duplicates to the EARLIEST row before
-- enforcing the invariant going forward. The extras' grants need no sweep: the
-- backfill joins members to spaces, so a grant whose space is gone is
-- never carried into IAM.
-- The index is PARTIAL (owner <> '') so any owner-less row — none created here — stays
-- unconstrained rather than colliding. Idempotent: no-op once converged.
DELETE FROM spaces WHERE owner <> '' AND id IN (
  SELECT w.id FROM spaces w
  WHERE EXISTS (
    SELECT 1 FROM spaces e
    WHERE e.owner_org = w.owner_org AND e.owner = w.owner AND e.id <> w.id
      AND (e.created_at < w.created_at OR (e.created_at = w.created_at AND e.id < w.id))
  )
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_spaces_org_owner ON spaces(owner_org, owner) WHERE owner <> '';
`
	if _, err := s.db.NewQuery(ddl).Execute(); err != nil {
		return fmt.Errorf("account migrate: %w", err)
	}
	return nil
}

// converge carries a deployment written before the table was called `spaces`.
//
// SQLite's ALTER … RENAME TO moves the indexes across with the table but LEAVES
// THEIR NAMES, so a converged file would carry ux_workspaces_uuid sitting on
// spaces — a name that describes nothing and that migrate's CREATE … IF NOT
// EXISTS would never replace, because the index it wants is absent under the
// name it looks for while the uniqueness is already enforced under another. They
// are dropped here and remade by the DDL above, so the file ends identical to
// one created fresh: same table, same four index names, no archaeology.
//
// Guarded on both ends. A fresh deployment has no old table and does nothing; a
// converged one has the new table and does nothing. Between them there is
// exactly one boot that renames, and it is the same boot either way because the
// whole thing runs inside openAccountStore before a single read.
func (s *accountStore) converge() error {
	if !s.hasTable("workspaces") || s.hasTable("spaces") {
		return nil
	}
	const ddl = `
ALTER TABLE workspaces RENAME TO spaces;
DROP INDEX IF EXISTS ux_workspaces_uuid;
DROP INDEX IF EXISTS ux_workspaces_org_slug;
DROP INDEX IF EXISTS ix_workspaces_org;
DROP INDEX IF EXISTS ux_workspaces_org_owner;
`
	if _, err := s.db.NewQuery(ddl).Execute(); err != nil {
		return fmt.Errorf("account converge: %w", err)
	}
	return nil
}

// hasTable reports whether one table is present. Asked rather than inferred from
// a failed read: "absent" and "the read broke" are the same error string and
// opposite situations, and only one of them may be converged over.
func (s *accountStore) hasTable(name string) bool {
	var n int
	err := s.db.NewQuery(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name={:name}`).
		Bind(query.Params{"name": name}).Row(&n)
	return err == nil && n > 0
}

// Close closes the underlying database. dbx.DB.Close closes the *sql.DB cek handed
// us, so the handle is owned in exactly one place.
func (s *accountStore) Close() error { return s.db.Close() }

// wsCols is the space projection — the columns every space read selects and
// the order EnsureSpace inserts by.
var wsCols = []string{"id", "slug", "name", "uuid", "owner", "owner_org", "data_id", "region", "created_at"}

// spacesIn starts a space read over a table expression. ONE spelling of
// "select a space", so a projection can never disagree with the struct.
func (s *accountStore) spacesIn(table string, cols ...string) *orm.Typed[space] {
	if len(cols) == 0 {
		cols = wsCols
	}
	return orm.Select[space](s.db, table, cols...)
}

// EnsureSpace gives an account a personal space in org if it has none, so
// the space picker is never empty. owner_org is the canonical tenant field
// every downstream surface (transactor path, roster reconcile) reads; `owner`
// keeps the creating account uuid for provenance only. Returns the account's
// (possibly pre-existing) first space in the org.
func (s *accountStore) EnsureSpace(ctx context.Context, org, account, name string) (space, error) {
	if org == "" || account == "" {
		return space{}, fmt.Errorf("team: ensure space: empty org/account")
	}
	// Fast path: the account already has a space → heal own membership, return it.
	if w, ok, err := s.adoptExisting(ctx, org, account); err != nil {
		return space{}, err
	} else if ok {
		return w, nil
	}
	if name == "" {
		name = "Space"
	}
	w := space{
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
		return space{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Idempotent create: the (owner_org, owner) partial-unique index makes a racing
	// second login's INSERT a no-op (RowsAffected 0). The winner also writes the owner
	// member row and commits; a loser rolls back its unused ids and ADOPTS the winner,
	// so concurrent logins converge to exactly ONE personal space.
	//
	// RAW, on two counts the builder has no client for: DO NOTHING (dbx's Upsert only
	// ever emits DO UPDATE SET) and a conflict target carrying the partial index's
	// WHERE. Written as a DO UPDATE it would invert the meaning — the loser would
	// overwrite the winner's row instead of yielding to it.
	res, err := tx.NewQuery(
		`INSERT INTO spaces (` + strings.Join(wsCols, ",") + `)
		 VALUES ({:id},{:slug},{:name},{:uuid},{:owner},{:owner_org},{:data_id},{:region},{:created_at})
		 ON CONFLICT(owner_org, owner) WHERE owner <> '' DO NOTHING`).
		Bind(query.Params{
			"id": w.ID, "slug": w.Slug, "name": w.Name, "uuid": w.UUID,
			"owner": w.Owner, "owner_org": w.OwnerOrg, "data_id": w.DataID,
			"region": w.Region, "created_at": w.CreatedAt,
		}).WithContext(ctx).Execute()
	if err != nil {
		return space{}, fmt.Errorf("insert space: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		if w, ok, err := s.adoptExisting(ctx, org, account); err != nil {
			return space{}, err
		} else if ok {
			return w, nil
		}
		return space{}, fmt.Errorf("team: ensure space: create conflicted but no personal space resolved")
	}
	if err := tx.Commit(); err != nil {
		return space{}, fmt.Errorf("commit: %w", err)
	}
	// The creator owns it, recorded where every other grant lives. AFTER the
	// commit, because the space has to exist before a grant can name it: the
	// grant is idempotent, so a failure here leaves a space its creator
	// re-owns on the next call rather than a row pointing at nothing.
	if err := s.AddMember(ctx, org, w.UUID, account, "owner"); err != nil {
		return space{}, fmt.Errorf("grant owner: %w", err)
	}
	return w, nil
}

// adoptExisting returns the account's first space in the org, if any.
//
// It heals nothing: the grant it used to repair is IAM's row now, and a space
// this account can be found through is one IAM already says they may act in.
func (s *accountStore) adoptExisting(ctx context.Context, org, account string) (space, bool, error) {
	existing, err := s.SpacesOf(ctx, org, account)
	if err != nil {
		return space{}, false, err
	}
	if len(existing) == 0 {
		return space{}, false, nil
	}
	return existing[0], true, nil
}

// SpacesOf returns the org's spaces the account may act in, newest first.
//
// Where a person may act is IAM's answer; which space carries which name is
// team's. So this asks IAM for the scopes and reads the rows for them, rather
// than joining a roster it no longer keeps.
func (s *accountStore) SpacesOf(ctx context.Context, org, account string) ([]space, error) {
	got, err := iam.IAMMembers(cloud.For(ctx, org), &client.Scope{User: account, Any: true})
	if err != nil {
		return nil, fmt.Errorf("spaces of: %w", err)
	}
	uuids := make([]any, 0, len(got.Memberships))
	for _, m := range got.Memberships {
		if m.Space != "" {
			uuids = append(uuids, m.Space)
		}
	}
	if len(uuids) == 0 {
		return nil, nil
	}
	out, err := s.spacesIn("spaces w", prefixed("w", wsCols)...).
		Where(query.HashExp{"w.owner_org": org, "w.uuid": uuids}).
		OrderBy("w.created_at DESC").
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("spaces of: %w", err)
	}
	return out, nil
}

// SpaceBySlug resolves a space by (org, slug) — the org scope is what stops
// a caller in org A from selecting org B's space by passing its slug. Returns
// errNoSpace when absent in the org.
func (s *accountStore) SpaceBySlug(ctx context.Context, org, slug string) (space, error) {
	w, ok, err := s.spacesIn("spaces").
		Where(query.HashExp{"owner_org": org, "slug": slug}).First(ctx)
	if err != nil {
		return space{}, fmt.Errorf("space by slug: %w", err)
	}
	if !ok {
		return space{}, errNoSpace
	}
	return w, nil
}

// SpaceByUUID resolves a space by (org, uuid) — the org scope is the
// tenant boundary the files plane relies on: a blob request naming another org's
// space uuid resolves to errNoSpace, so it can never reach that org's
// blobs. Returns errNoSpace when absent in the org.
func (s *accountStore) SpaceByUUID(ctx context.Context, org, id string) (space, error) {
	w, ok, err := s.spacesIn("spaces").
		Where(query.HashExp{"owner_org": org, "uuid": id}).First(ctx)
	if err != nil {
		return space{}, fmt.Errorf("space by uuid: %w", err)
	}
	if !ok {
		return space{}, errNoSpace
	}
	return w, nil
}

// Membership is the role a person holds in a space, from IAM.
//
// Team keeps no roster: identity, membership and roles are IAM's. The scope is
// the space UUID — team's stable external name for it, which is what the
// grant is filed against.
func (s *accountStore) Membership(ctx context.Context, org, wsUUID, account string) (string, bool) {
	for _, m := range s.roster(ctx, org, wsUUID) {
		if m.User == account {
			return m.Role, true
		}
	}
	return "", false
}

// MemberName is the name IAM holds for a member of a space.
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
	got, err := iam.IAMMembers(cloud.For(ctx, org), &client.Scope{User: account, Any: true})
	if err != nil || len(got.Memberships) == 0 {
		return "", false
	}
	return account, true
}

// MembersForSpaceUUID is the space's roster, from IAM.
func (s *accountStore) MembersForSpaceUUID(ctx context.Context, org, wsUUID string) ([]member, error) {
	rows := s.roster(ctx, org, wsUUID)
	out := make([]member, 0, len(rows))
	for _, m := range rows {
		out = append(out, member{
			SpaceID: wsUUID, UserID: m.User, Role: m.Role,
			DisplayName: m.Name, Active: true,
		})
	}
	return out, nil
}

// roster reads one space's memberships. It answers empty on a failure: every
// caller is deciding whether ONE person may act, and an empty roster denies.
func (s *accountStore) roster(ctx context.Context, org, wsUUID string) []client.Membership {
	got, err := iam.IAMMembers(cloud.For(ctx, org), &client.Scope{Space: wsUUID})
	if err != nil {
		return nil
	}
	return got.Memberships
}

// GuestRank is a guest's position among the space's guests, oldest first.
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

// AddMember records in IAM that account may act in the space.
func (s *accountStore) AddMember(ctx context.Context, org, wsUUID, account, role string) error {
	if wsUUID == "" || account == "" {
		return fmt.Errorf("team: add member: empty space/account")
	}
	if role == "" {
		role = "member"
	}
	_, err := iam.IAMGrant(cloud.For(ctx, org), &client.GrantIn{
		User: account, Space: wsUUID, Role: role,
	})
	return err
}

// SpacesForOrg returns every space of an org — used by /v1/team/bots/sync
// to re-project the org's agents into each of its spaces. Org-scoped.
func (s *accountStore) SpacesForOrg(ctx context.Context, org string) ([]space, error) {
	out, err := s.spacesIn("spaces").
		Where(query.HashExp{"owner_org": org}).
		OrderBy("created_at DESC").All(ctx)
	if err != nil {
		return nil, fmt.Errorf("spaces for org: %w", err)
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
