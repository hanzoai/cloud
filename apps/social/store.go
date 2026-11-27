package social

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"

	// The ONE Hanzo "sqlite" driver; blank import registers it.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409.
var (
	errNotFound = errors.New("social: not found")
	errConflict = errors.New("social: already exists")
)

// subsystem names the store: the system namespace's "social". The database and the sealer
// are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "social"

// social_accounts.token, the account's provider access token, is a KMS value
// (internal/escrow), kept at orgs/<org>/social/<account id>/token@default; the column holds
// that path and no byte of the token. Whoever holds the token can post as the customer.
// Left plain: ids, the provider and status vocabularies, timestamps, and handle (the
// account's public name on the network, which the customer's own audience already sees and
// the listing orders beside).
//
// A file from before holds the token in plaintext or sealed as a field (internal/org),
// bound to the row's (org, id). The open writes each to KMS and replaces the column with its
// path; a row that holds a path is left alone, so a second open does nothing.
var accountKey = []string{"org", "id"}

// Store is the social database. ONE database — the deployment's own "social"
// subsystem, a SQLite file or a schema on hanzo-sql — holds every org's records;
// tenant isolation is the `org` column, enforced on EVERY query. This mirrors
// clients/crm exactly (the ONE storage pattern). A file's one connection serializes
// writes; on hanzo-sql the claim below is a conditional UPDATE, which the row lock
// serializes.
//
// Its SQL is the dialect both engines share (sdk/dialect) and its tests run
// on both (internal/sqltest).
type Store struct {
	db     *sql.DB
	d      dialect.Dialect
	fields *tenure.Fields // opens a token a file held before KMS; nil on a process with no data-plane master
	esc    *escrow.Escrow // keeps the tokens
}

// openStore opens the database and moves what it held before KMS into KMS. master is
// cloud.Deps.MasterKey and opens only that old value; k is cloud.Deps.KMS and keeps every
// token. With no KMS an account's token is neither kept nor read.
func openStore(dir string, master []byte, k escrow.KMS) (*Store, error) {
	var fields *tenure.Fields
	if c, err := tenure.NewCipher(master); err == nil {
		if fields, err = c.Fields(namespace.System(), subsystem); err != nil {
			return nil, fmt.Errorf("open social store: %w", err)
		}
	}
	s, err := locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db), fields: fields, esc: escrow.New(k, "social")}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		if err := s.moveToKMS(ctx); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open social store: %w", err)
	}
	return s, nil
}

// migrate creates the accounts + posts tables. Idempotent (IF NOT EXISTS). Each
// table leads its lookup indexes with `org` so tenant isolation is a physical
// property, not just a WHERE clause. The one exception is
// ix_social_posts_status_schedule: the scheduler's due-post sweep is the ONE
// system-level (cross-org) read (see DueScheduled), so it is led by (status,
// schedule_at), never by org.
func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS social_accounts (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  provider       TEXT NOT NULL DEFAULT 'x',
  handle         TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'connected',
  token          TEXT NOT NULL DEFAULT '',
  external_id    TEXT NOT NULL DEFAULT '',
  name           TEXT NOT NULL DEFAULT '',
  picture        TEXT NOT NULL DEFAULT '',
  profile        TEXT NOT NULL DEFAULT '',
  group_id       TEXT NOT NULL DEFAULT '',
  group_name     TEXT NOT NULL DEFAULT '',
  refresh_needed BIGINT NOT NULL DEFAULT 0,
  disabled       BIGINT NOT NULL DEFAULT 0,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_accounts_org_updated  ON social_accounts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_social_accounts_org_provider ON social_accounts(org, provider);

CREATE TABLE IF NOT EXISTS social_pending_groups (
  org        TEXT NOT NULL,
  provider   TEXT NOT NULL,
  group_name TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  PRIMARY KEY (org, provider)
);

CREATE TABLE IF NOT EXISTS social_posts (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  content      TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL DEFAULT 'x',
  status       TEXT NOT NULL DEFAULT 'draft',
  schedule_at  BIGINT NOT NULL DEFAULT 0,
  account_id   TEXT NOT NULL DEFAULT '',
  external_id  TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  media        TEXT NOT NULL DEFAULT '[]',
  origin       TEXT NOT NULL DEFAULT 'native',
  external_id_postiz TEXT NOT NULL DEFAULT '',
  external_integration_id TEXT NOT NULL DEFAULT '',
  external_group TEXT NOT NULL DEFAULT '',
  release_url  TEXT NOT NULL DEFAULT '',
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_updated       ON social_posts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_status        ON social_posts(org, status);
CREATE INDEX IF NOT EXISTS ix_social_posts_org_schedule      ON social_posts(org, schedule_at);
CREATE INDEX IF NOT EXISTS ix_social_posts_status_schedule   ON social_posts(status, schedule_at);
CREATE TABLE IF NOT EXISTS social_events (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  payload     TEXT NOT NULL DEFAULT '{}',
  created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_social_events_org_name ON social_events(org, name);
CREATE UNIQUE INDEX IF NOT EXISTS ix_social_events_dedup ON social_events(org, name, payload);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("social migrate: %w", err)
	}
	// Idempotent column upgrades for a pre-existing DB created before publishing
	// landed (fresh installs already have them via the DDL above). Each column is
	// added once, found by asking the table for it.
	for _, ac := range []struct{ table, col, def string }{
		{"social_accounts", "token", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "external_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "name", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "picture", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "profile", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "group_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "group_name", "TEXT NOT NULL DEFAULT ''"},
		{"social_accounts", "refresh_needed", "BIGINT NOT NULL DEFAULT 0"},
		{"social_accounts", "disabled", "BIGINT NOT NULL DEFAULT 0"},
		{"social_posts", "account_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "external_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "error", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "media", "TEXT NOT NULL DEFAULT '[]'"},
		{"social_posts", "origin", "TEXT NOT NULL DEFAULT 'native'"},
		{"social_posts", "external_id_postiz", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "external_integration_id", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "external_group", "TEXT NOT NULL DEFAULT ''"},
		{"social_posts", "release_url", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumn(ctx, tx, ac.table, ac.col, ac.def); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS ix_social_accounts_ref ON social_accounts(org, provider, external_id) WHERE external_id <> ''`); err != nil {
		return fmt.Errorf("social migrate: account ref: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS ix_social_posts_postiz ON social_posts(org, origin, external_id_postiz, external_integration_id) WHERE origin='postiz' AND external_id_postiz <> ''`); err != nil {
		return fmt.Errorf("social migrate: postiz ref: %w", err)
	}
	return nil
}

// moveToKMS writes, once, the tokens a file holds from before KMS (plain, or sealed as a
// field) to KMS and replaces each column with its path. It runs outside the schema's
// transaction, because it calls KMS; it compares what it read, so another replica that
// moved the row first leaves nothing to change.
func (s *Store) moveToKMS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, token FROM social_accounts WHERE token <> '' AND substr(token, 1, 4) <> 'kms:'`)
	if err != nil {
		return fmt.Errorf("social migrate: %w", err)
	}
	type held struct{ id, org, token string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.org, &h.token); err != nil {
			_ = rows.Close()
			return fmt.Errorf("social migrate: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("social migrate: %w", err)
	}
	for _, h := range found {
		token := h.token
		if strings.HasPrefix(token, "seal:") {
			if s.fields == nil {
				return fmt.Errorf("social migrate: account %s: no data-plane master on this process, so a token the file held before KMS cannot be opened", h.id)
			}
			r := s.fields.Row("social_accounts", h.org, h.id)
			if token = r.Open("token", token); r.Err() != nil {
				return fmt.Errorf("social migrate: account %s: %w", h.id, r.Err())
			}
		}
		ref, err := s.esc.Put(ctx, h.org, h.id, "token", token)
		if err != nil {
			return fmt.Errorf("social migrate: account %s: %w", h.id, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE social_accounts SET token = $1 WHERE id = $2 AND org = $3 AND token = $4`,
			ref, h.id, h.org, h.token); err != nil {
			return fmt.Errorf("social migrate: %w", err)
		}
	}
	return nil
}

// addColumn adds a column to an existing table, and does nothing when the table
// already has it (neither engine's ADD COLUMN both skips one that is there and
// parses on the other).
func (s *Store) addColumn(ctx context.Context, tx *sql.Tx, table, col, def string) error {
	have, err := s.d.HasColumn(ctx, tx, table, col)
	if err != nil {
		return fmt.Errorf("social migrate: look for %s.%s: %w", table, col, err)
	}
	if have {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def)); err != nil {
		return fmt.Errorf("social migrate: add %s.%s: %w", table, col, err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ---- accounts ----

// socialAccount is an org-scoped connected social account (a Postiz-style "integration"
// on the live social stack). Provider is the network (x/facebook/instagram/…);
// Status is the connection lifecycle (connected/disconnected/error) — both
// validated at the write layer against the fixed vocabularies in social.go.
type socialAccount struct {
	// ID is the account's identifier, minted on connect and the id every later
	// call addresses it by.
	//
	// Example: "acct_7f3c1a"
	ID string `json:"id"`
	// Org is the tenant the account belongs to, resolved server-side from the
	// validated principal. It never crosses the wire in either direction.
	Org string `json:"-"`
	// Provider is the network this account is on: x, facebook, instagram,
	// linkedin, tiktok, youtube or threads.
	//
	// Example: "x"
	Provider string `json:"provider"`
	// Handle is the account's public name on the network, as the customer knows
	// it. Trimmed and bounded at 1024 characters.
	//
	// Example: "@acme"
	Handle string `json:"handle"`
	// Status is the connection lifecycle: connected, disconnected or error. Only
	// a connected account is a publish target.
	Status string `json:"status"`
	// Token is the account's provider access token (written by the connect/OAuth
	// flow, which has NOT landed yet), carried by a create and by nothing a store reads:
	// it is NEVER serialized to an API response (`json:"-"`), so it cannot leak to the
	// browser. At rest it is a KMS value, and the row holds only its path; the publisher
	// asks [Store.Token] for it. (The live Postiz stack stores this token in PLAINTEXT in
	// Postgres.)
	Token string `json:"-"`
	// tokenRef is the path the row holds in place of the token.
	tokenRef string
	// CreatedAt is when the account was connected, as a unix timestamp in seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the account row last changed, as a unix timestamp in
	// seconds. The listing is ordered by it, newest first.
	UpdatedAt int64 `json:"updatedAt"`
	// ExternalID is the platform id the sealed token is stored under. Empty for
	// an account recorded by hand, before a consent named one. A publish uses
	// it to choose that token rather than another account on the same network.
	ExternalID string `json:"externalId,omitempty"`
	// Name is the channel's human-readable display name on the network (for
	// example "Antje Worring" or "Hanzo AI"). Optional.
	Name string `json:"name,omitempty"`
	// Picture is the avatar URL of the connected channel profile, when reported
	// by the network. Optional.
	Picture string `json:"picture,omitempty"`
	// Profile is the canonical profile identifier or URL slug on the network.
	// Optional.
	Profile string `json:"profile,omitempty"`
	// GroupID is the deterministic identifier of the employee or brand channel
	// group this channel is organized under. Optional.
	GroupID string `json:"groupId,omitempty"`
	// GroupName is the employee or brand group label (for example "Antje Worring",
	// "Zach Kelling", or "Hanzo AI") used to organize and fan out posts across a
	// person's or team's channels. Optional.
	GroupName string `json:"groupName,omitempty"`
	// RefreshNeeded is true when the upstream OAuth token has expired or requires
	// interactive reconnection before publishing.
	RefreshNeeded bool `json:"refreshNeeded,omitempty"`
	// Disabled is true when posting to this channel has been paused without
	// disconnecting its stored credentials.
	Disabled bool `json:"disabled,omitempty"`
}

const accountCols = `id,org,provider,handle,status,token,created_at,updated_at,external_id,name,picture,profile,group_id,group_name,refresh_needed,disabled`

// scanAccount reads a row. It names the account's token and does not read it: a listing
// asks KMS for nothing.
func (s *Store) scanAccount(sc interface{ Scan(...any) error }) (socialAccount, error) {
	var a socialAccount
	var refreshInt, disabledInt int64
	if err := sc.Scan(
		&a.ID, &a.Org, &a.Provider, &a.Handle, &a.Status, &a.tokenRef,
		&a.CreatedAt, &a.UpdatedAt, &a.ExternalID,
		&a.Name, &a.Picture, &a.Profile, &a.GroupID, &a.GroupName,
		&refreshInt, &disabledInt,
	); err != nil {
		return a, err
	}
	a.RefreshNeeded = refreshInt != 0
	a.Disabled = disabledInt != 0
	return a, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func groupIDFor(org, groupName string) string {
	g := strings.TrimSpace(groupName)
	if g == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(org + "\x00group\x00" + strings.ToLower(g)))
	return "grp_" + hex.EncodeToString(sum[:8])
}

// Token is the provider access token KMS holds for a, or "" for an account that has none.
// Only the publisher asks.
func (s *Store) Token(ctx context.Context, a socialAccount) (string, error) {
	return s.esc.Get(ctx, a.Org, a.tokenRef)
}

// CreateAccount inserts an account. A token it carries is kept in KMS first and the row
// names it; a row that cannot be written forgets the token again.
func (s *Store) CreateAccount(ctx context.Context, a socialAccount) (socialAccount, error) {
	if a.GroupName != "" && a.GroupID == "" {
		a.GroupID = groupIDFor(a.Org, a.GroupName)
	}
	if err := dialect.Text(a.ID, a.Org, a.Provider, a.Handle, a.Status, a.Token, a.ExternalID, a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName); err != nil {
		return socialAccount{}, err
	}
	ref, err := s.esc.Put(ctx, a.Org, a.ID, "token", a.Token)
	if err != nil {
		return socialAccount{}, fmt.Errorf("insert account: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_accounts (`+accountCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		a.ID, a.Org, a.Provider, a.Handle, a.Status, ref, a.CreatedAt, a.UpdatedAt, a.ExternalID,
		a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName, boolInt(a.RefreshNeeded), boolInt(a.Disabled)); err != nil {
		_ = s.esc.Drop(ctx, a.Org, ref)
		return socialAccount{}, fmt.Errorf("insert account: %w", err)
	}
	a.tokenRef = ref
	return a, nil
}

// ListConnectedAccounts returns an org's CONNECTED accounts for one provider — the
// publish targets for a post on that channel. status='connected' only (a disconnected
// or errored account is never a publish target). Org-scoped like every other read.
func (s *Store) ListConnectedAccounts(ctx context.Context, org, provider string) ([]socialAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountCols+` FROM social_accounts WHERE org=$1 AND provider=$2 AND status='connected' AND disabled=0 ORDER BY updated_at DESC`,
		org, provider)
	if err != nil {
		return nil, fmt.Errorf("list connected accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]socialAccount, 0, 4)
	for rows.Next() {
		a, err := s.scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAccount(ctx context.Context, org, id string) (socialAccount, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM social_accounts WHERE org=$1 AND id=$2`, org, id)
	a, err := s.scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return socialAccount{}, errNotFound
	}
	if err != nil {
		return socialAccount{}, fmt.Errorf("get account: %w", err)
	}
	return a, nil
}

// ListAccounts lists the org's accounts, optionally filtered by provider
// (provider=="" means all). Most-recently-updated first.
func (s *Store) ListAccounts(ctx context.Context, org, provider string, limit int) ([]socialAccount, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if provider == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+accountCols+` FROM social_accounts WHERE org=$1 ORDER BY updated_at DESC LIMIT $2`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+accountCols+` FROM social_accounts WHERE org=$1 AND provider=$2 ORDER BY updated_at DESC LIMIT $3`, org, provider, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]socialAccount, 0, 16)
	for rows.Next() {
		a, err := s.scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAccount(ctx context.Context, a socialAccount) (socialAccount, error) {
	if err := dialect.Text(a.Provider, a.Handle, a.Status); err != nil {
		return socialAccount{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_accounts SET provider=$1,handle=$2,status=$3,updated_at=$4 WHERE org=$5 AND id=$6`,
		a.Provider, a.Handle, a.Status, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return socialAccount{}, fmt.Errorf("update account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return socialAccount{}, errNotFound
	}
	return s.GetAccount(ctx, a.Org, a.ID)
}

// UpsertTarget records the account a consent produced, keyed by the platform
// id. Connecting the same account again updates the handle and leaves the id
// the org already manages. A different platform id is a different row, so a
// page cannot replace a profile.
func (s *Store) UpsertTarget(ctx context.Context, org, provider, externalID, handle string, now int64) (socialAccount, error) {
	if externalID == "" {
		return socialAccount{}, fmt.Errorf("social: account has no platform id")
	}
	id := idFor(org, provider, externalID)
	groupName := s.consumePendingGroup(ctx, org, provider)
	groupID := groupIDFor(org, groupName)
	if err := dialect.Text(org, provider, externalID, handle, groupID, groupName); err != nil {
		return socialAccount{}, err
	}
	// One statement, so two consents for one platform id cannot both find none and both
	// insert: the second lands on the unique reference and updates the first's row. An
	// account recorded by hand keeps the id it has; the returned row says which it is.
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO social_accounts (`+accountCols+`) VALUES ($1,$2,$3,$4,'connected','',$5,$5,$6,'','','',$7,$8,0,0)
		 ON CONFLICT (org, provider, external_id) WHERE external_id <> '' DO UPDATE SET
		   handle=excluded.handle,
		   status='connected',
		   refresh_needed=0,
		   group_id=CASE WHEN excluded.group_name <> '' THEN excluded.group_id ELSE social_accounts.group_id END,
		   group_name=CASE WHEN excluded.group_name <> '' THEN excluded.group_name ELSE social_accounts.group_name END,
		   updated_at=excluded.updated_at
		 RETURNING `+accountCols,
		id, org, provider, handle, now, externalID, groupID, groupName)
	a, err := s.scanAccount(row)
	if err != nil {
		return socialAccount{}, fmt.Errorf("upsert target: %w", err)
	}
	return a, nil
}

// PutPendingGroup remembers the employee or brand group requested when starting
// an OAuth connect flow so the callback's UpsertTarget assigns it automatically.
func (s *Store) PutPendingGroup(ctx context.Context, org, provider, groupName string, now int64) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil
	}
	if err := dialect.Text(org, provider, groupName); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO social_pending_groups (org, provider, group_name, created_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (org, provider) DO UPDATE SET group_name=excluded.group_name, created_at=excluded.created_at`,
		org, provider, groupName, now)
	return err
}

func (s *Store) consumePendingGroup(ctx context.Context, org, provider string) string {
	var g string
	if err := s.db.QueryRowContext(ctx,
		`SELECT group_name FROM social_pending_groups WHERE org=$1 AND provider=$2`, org, provider).Scan(&g); err != nil {
		return ""
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM social_pending_groups WHERE org=$1 AND provider=$2`, org, provider)
	return strings.TrimSpace(g)
}

// UpsertSyncedChannel mirrors a channel discovered from post.hanzo.ai into
// social_accounts using its exact channel id so API, CLI, MCP, and Web UI
// share identical channel IDs and employee/brand groups.
func (s *Store) UpsertSyncedChannel(ctx context.Context, a socialAccount) (socialAccount, error) {
	if a.GroupName != "" && a.GroupID == "" {
		a.GroupID = groupIDFor(a.Org, a.GroupName)
	}
	if err := dialect.Text(a.ID, a.Org, a.Provider, a.Handle, a.Status, a.ExternalID, a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName); err != nil {
		return socialAccount{}, err
	}
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO social_accounts (`+accountCols+`) VALUES ($1,$2,$3,$4,$5,'',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 ON CONFLICT (id) DO UPDATE SET
		   provider=excluded.provider,
		   handle=excluded.handle,
		   status=excluded.status,
		   external_id=excluded.external_id,
		   name=excluded.name,
		   picture=excluded.picture,
		   profile=excluded.profile,
		   group_id=CASE WHEN excluded.group_name <> '' THEN excluded.group_id ELSE social_accounts.group_id END,
		   group_name=CASE WHEN excluded.group_name <> '' THEN excluded.group_name ELSE social_accounts.group_name END,
		   refresh_needed=excluded.refresh_needed,
		   disabled=excluded.disabled,
		   updated_at=excluded.updated_at
		 RETURNING `+accountCols,
		a.ID, a.Org, a.Provider, a.Handle, a.Status, a.CreatedAt, a.UpdatedAt, a.ExternalID,
		a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName, boolInt(a.RefreshNeeded), boolInt(a.Disabled))
	out, err := s.scanAccount(row)
	if err != nil {
		return socialAccount{}, fmt.Errorf("upsert synced channel: %w", err)
	}
	return out, nil
}

// ListChannelsFiltered lists an org's channels filtered optionally by provider
// and/or group (matching either group_id or case-insensitive group_name).
func (s *Store) ListChannelsFiltered(ctx context.Context, org, provider, group string, limit int) ([]socialAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountCols+` FROM social_accounts
		 WHERE org=$1
		   AND ($2 = '' OR provider = $2)
		   AND ($3 = '' OR group_id = $3 OR LOWER(group_name) = LOWER($3))
		 ORDER BY updated_at DESC LIMIT $4`,
		org, provider, strings.TrimSpace(group), limit)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]socialAccount, 0, 16)
	for rows.Next() {
		a, err := s.scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type channelGroupSummary struct {
	ID       string
	Name     string
	Channels int
}

// ListChannelGroups aggregates the distinct non-empty employee/brand groups on
// an org's channels with their channel counts.
func (s *Store) ListChannelGroups(ctx context.Context, org string) ([]channelGroupSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_id, group_name, COUNT(*)
		 FROM social_accounts
		 WHERE org=$1 AND group_name <> ''
		 GROUP BY group_id, group_name
		 ORDER BY group_name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list channel groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]channelGroupSummary, 0, 8)
	for rows.Next() {
		var g channelGroupSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.Channels); err != nil {
			return nil, fmt.Errorf("scan channel group: %w", err)
		}
		if g.ID == "" {
			g.ID = groupIDFor(org, g.Name)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetChannelGroup assigns or clears (when groupName=="") a channel's group.
func (s *Store) SetChannelGroup(ctx context.Context, org, id, groupName string, now int64) (socialAccount, error) {
	groupName = strings.TrimSpace(groupName)
	groupID := groupIDFor(org, groupName)
	if err := dialect.Text(groupID, groupName); err != nil {
		return socialAccount{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_accounts SET group_id=$1, group_name=$2, updated_at=$3 WHERE org=$4 AND id=$5`,
		groupID, groupName, now, org, id)
	if err != nil {
		return socialAccount{}, fmt.Errorf("set channel group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return socialAccount{}, errNotFound
	}
	return s.GetAccount(ctx, org, id)
}

// UpdateChannel updates a channel's metadata, status, disabled flag, and group.
func (s *Store) UpdateChannel(ctx context.Context, a socialAccount) (socialAccount, error) {
	if a.GroupName != "" {
		a.GroupID = groupIDFor(a.Org, a.GroupName)
	} else {
		a.GroupID = ""
	}
	if err := dialect.Text(a.Provider, a.Handle, a.Status, a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName); err != nil {
		return socialAccount{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_accounts
		 SET provider=$1, handle=$2, status=$3, name=$4, picture=$5, profile=$6, group_id=$7, group_name=$8, disabled=$9, updated_at=$10
		 WHERE org=$11 AND id=$12`,
		a.Provider, a.Handle, a.Status, a.Name, a.Picture, a.Profile, a.GroupID, a.GroupName, boolInt(a.Disabled), a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return socialAccount{}, fmt.Errorf("update channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return socialAccount{}, errNotFound
	}
	return s.GetAccount(ctx, a.Org, a.ID)
}

func idFor(org, provider, externalID string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + provider + "\x00" + externalID))
	return "acct_" + hex.EncodeToString(sum[:8])
}

// DeleteTarget forgets the account a platform id names. Org-scoped; none listed is
// not an error.
func (s *Store) DeleteTarget(ctx context.Context, org, provider, externalID string) error {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM social_accounts WHERE org=$1 AND provider=$2 AND external_id=$3`, org, provider, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete target: %w", err)
	}
	_, err = s.DeleteAccount(ctx, org, id)
	return err
}

// DeleteAccount removes an account and asks KMS to forget its token.
func (s *Store) DeleteAccount(ctx context.Context, org, id string) (bool, error) {
	var ref string
	err := s.db.QueryRowContext(ctx, `SELECT token FROM social_accounts WHERE org=$1 AND id=$2`, org, id).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete account: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM social_accounts WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := s.esc.Drop(ctx, org, ref); err != nil {
		return true, fmt.Errorf("delete account: the account is gone but KMS still holds its token: %w", err)
	}
	return true, nil
}

// ---- posts ----

// socialPost is an org-scoped social post. Channel is the target network
// (x/facebook/instagram/…); Status is the lifecycle (draft/scheduled/published/
// failed). ScheduleAt is a unix timestamp (0 = not scheduled / publish now) — a
// post with Status=="scheduled" carries the future ScheduleAt. All validated at
// the write layer against the fixed vocabularies in social.go.
type socialPost struct {
	// ID is the post's identifier, minted on create and the id every later call
	// addresses it by.
	//
	// Example: "post_91ab20"
	ID string `json:"id"`
	// Org is the tenant the post belongs to, resolved server-side from the
	// validated principal. It never crosses the wire in either direction.
	Org string `json:"-"`
	// Content is the post's text, bounded at 8192 characters.
	//
	// Example: "Shipping today."
	Content string `json:"content"`
	// Channel is the network this post targets: x, facebook, instagram, linkedin,
	// tiktok, youtube or threads. It is the channel whose CONNECTED accounts a
	// publish fans out to.
	//
	// Example: "x"
	Channel string `json:"channel"`
	// Status is the post's lifecycle state: draft, scheduled, published or failed.
	// A fifth, transient publishing state exists while a publish attempt holds the
	// claim; it is never settable from a request and a caller sees it only if it
	// reads a post mid-attempt.
	Status string `json:"status"`
	// ScheduleAt is when the post is due, as a unix timestamp in SECONDS. 0 means
	// unscheduled. It is meaningful only while the status is scheduled — a
	// scheduled post whose time has arrived is published by the scheduler.
	ScheduleAt int64 `json:"scheduleAt"`
	// Media is the post's attached media as a list of URLs (images today; the
	// composer's URL field now, an S3 picker later, populate it). Stored as a JSON
	// array in the media TEXT column and ALWAYS serialized as an array (never null),
	// so a client can rely on `media` being present. Bounded at the write layer
	// (normMedia in social.go): each URL clipped to maxField, the list to maxMedia.
	Media []string `json:"media"`
	// AccountID / ExternalID / Error are server-managed publish results, set only by
	// the publish path (never by a client update): the account a post was published
	// through, the provider's returned external post id (for reconciliation), and the
	// last failure reason. Empty until a publish attempt lands.
	// AccountID is the connected account the post went out through. Absent until
	// a publish succeeds.
	AccountID string `json:"accountId,omitempty"`
	// ExternalID is the id the network returned for the published post, which is
	// what reconciles this row against the post on the network. Absent until a
	// publish succeeds.
	ExternalID string `json:"externalId,omitempty"`
	// Error is why the last publish attempt failed, verbatim and bounded. Absent
	// when no attempt has failed; cleared by a later success. A post published to
	// several accounts that only some took is published, and this names the rest.
	Error string `json:"error,omitempty"`
	// Origin is who owns the post: "native" (a Hanzo user created it) or
	// "postiz" (mirrored from Postiz via ingest/poller). Mirrored posts are
	// read-only — clients may not create, update, publish or schedule them.
	Origin string `json:"origin,omitempty"`
	// ExternalIDPostiz is the Postiz post id, set only for origin=postiz rows.
	ExternalIDPostiz string `json:"externalIdPostiz,omitempty"`
	// ExternalIntegrationId is the Postiz integration id that published it.
	ExternalIntegrationId string `json:"externalIntegrationId,omitempty"`
	// ExternalGroup is the Postiz group the post belongs to (optional).
	ExternalGroup string `json:"externalGroup,omitempty"`
	// ReleaseURL is the live URL Postiz returned for the published post.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// CreatedAt is when the post was created, as a unix timestamp in seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the post row last changed, as a unix timestamp in seconds.
	// The listing is ordered by it, newest first.
	UpdatedAt int64 `json:"updatedAt"`
}

const postCols = `id,org,content,channel,status,schedule_at,account_id,external_id,error,media,origin,external_id_postiz,external_integration_id,external_group,release_url,created_at,updated_at`

func scanPost(sc interface{ Scan(...any) error }) (socialPost, error) {
	var p socialPost
	var media string
	err := sc.Scan(&p.ID, &p.Org, &p.Content, &p.Channel, &p.Status, &p.ScheduleAt,
		&p.AccountID, &p.ExternalID, &p.Error, &media,
		&p.Origin, &p.ExternalIDPostiz, &p.ExternalIntegrationId, &p.ExternalGroup, &p.ReleaseURL,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Media = decodeMedia(media)
	return p, nil
}

// decodeMedia parses the stored JSON media array into a non-nil (possibly empty)
// slice, so a post always serializes `media` as [] rather than null. A malformed or
// empty stored value degrades to [] rather than erroring a read.
func decodeMedia(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return []string{}
	}
	out := []string{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}

// encodeMedia serializes a post's media URLs to the JSON stored in the media column.
// A nil/empty slice stores the canonical "[]" (matching the column default).
func encodeMedia(m []string) string {
	if len(m) == 0 {
		return "[]"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// postText refuses a post whose text one engine could not store (dialect.Text),
// before a statement is written for it. Media is JSON, which escapes what it holds.
func postText(p socialPost) error {
	return dialect.Text(p.ID, p.Org, p.Content, p.Channel, p.Status, p.AccountID, p.ExternalID, p.Error,
		p.Origin, p.ExternalIDPostiz, p.ExternalIntegrationId, p.ExternalGroup, p.ReleaseURL)
}

func (s *Store) CreatePost(ctx context.Context, p socialPost) (socialPost, error) {
	if p.Origin == "" {
		p.Origin = "native"
	}
	if err := postText(p); err != nil {
		return socialPost{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO social_posts (`+postCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		p.ID, p.Org, p.Content, p.Channel, p.Status, p.ScheduleAt,
		p.AccountID, p.ExternalID, p.Error, encodeMedia(p.Media),
		p.Origin, p.ExternalIDPostiz, p.ExternalIntegrationId, p.ExternalGroup, p.ReleaseURL,
		p.CreatedAt, p.UpdatedAt); err != nil {
		return socialPost{}, fmt.Errorf("insert post: %w", err)
	}
	if p.Media == nil {
		p.Media = []string{}
	}
	return p, nil
}

func (s *Store) GetPost(ctx context.Context, org, id string) (socialPost, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+postCols+` FROM social_posts WHERE org=$1 AND id=$2`, org, id)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return socialPost{}, errNotFound
	}
	if err != nil {
		return socialPost{}, fmt.Errorf("get post: %w", err)
	}
	return p, nil
}

// ListPosts lists the org's posts, optionally filtered by status (status=="" means
// all). Most-recently-updated first.
func (s *Store) ListPosts(ctx context.Context, org, status string, limit int) ([]socialPost, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+postCols+` FROM social_posts WHERE org=$1 ORDER BY updated_at DESC LIMIT $2`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+postCols+` FROM social_posts WHERE org=$1 AND status=$2 ORDER BY updated_at DESC LIMIT $3`, org, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list posts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]socialPost, 0, 16)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePost(ctx context.Context, p socialPost) (socialPost, error) {
	if err := postText(p); err != nil {
		return socialPost{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET content=$1,channel=$2,status=$3,schedule_at=$4,media=$5,account_id=$6,updated_at=$7 WHERE org=$8 AND id=$9`,
		p.Content, p.Channel, p.Status, p.ScheduleAt, encodeMedia(p.Media), p.AccountID, p.UpdatedAt, p.Org, p.ID)
	if err != nil {
		return socialPost{}, fmt.Errorf("update post: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return socialPost{}, errNotFound
	}
	return s.GetPost(ctx, p.Org, p.ID)
}

// ListPostsFiltered lists an org's posts filtered optionally by status,
// channel (provider), accountId (or external_integration_id), and group.
func (s *Store) ListPostsFiltered(ctx context.Context, org, status, channel, accountID, group string, limit int) ([]socialPost, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+postCols+` FROM social_posts
		 WHERE org=$1
		   AND ($2 = '' OR status = $2)
		   AND ($3 = '' OR channel = $3)
		   AND ($4 = '' OR account_id = $4 OR external_integration_id = $4)
		   AND ($5 = '' OR external_group = $5 OR account_id IN (
		         SELECT id FROM social_accounts WHERE org=$1 AND (group_id=$5 OR LOWER(group_name)=LOWER($5))
		       ) OR external_integration_id IN (
		         SELECT id FROM social_accounts WHERE org=$1 AND (group_id=$5 OR LOWER(group_name)=LOWER($5))
		       ))
		 ORDER BY updated_at DESC LIMIT $6`,
		org, strings.TrimSpace(status), strings.TrimSpace(channel), strings.TrimSpace(accountID), strings.TrimSpace(group), limit)
	if err != nil {
		return nil, fmt.Errorf("list posts filtered: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]socialPost, 0, 16)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ChannelPostCounts returns post delivery counts attributed to one channel id.
func (s *Store) ChannelPostCounts(ctx context.Context, org, channelID, provider string) (posts, published, scheduled, failed int, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status IN ('published','mirrored') THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(CASE WHEN status='scheduled' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0)
		 FROM social_posts
		 WHERE org=$1 AND (account_id=$2 OR external_integration_id=$2 OR (account_id='' AND external_integration_id='' AND channel=$3))`,
		org, channelID, provider)
	if err = row.Scan(&posts, &published, &scheduled, &failed); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("channel post counts: %w", err)
	}
	return posts, published, scheduled, failed, nil
}

// UpsertMirrored creates or replaces a mirrored post keyed by (org, origin,
// external_id_postiz, external_integration_id). Returns the stored row and
// whether it was inserted (true) or updated (false).
func (s *Store) UpsertMirrored(ctx context.Context, p socialPost) (socialPost, bool, error) {
	if p.Origin != "postiz" || p.Status != statusMirrored {
		return socialPost{}, false, fmt.Errorf("social: upsert mirrored requires origin=postiz and status=mirrored, got origin=%q status=%q", p.Origin, p.Status)
	}
	if p.ExternalIDPostiz == "" || p.ExternalIntegrationId == "" {
		return socialPost{}, false, fmt.Errorf("social: upsert mirrored requires external ids")
	}
	if err := postText(p); err != nil {
		return socialPost{}, false, err
	}
	// Try insert first. If the unique postiz index blocks it, fall through to an
	// in-place update of the existing row.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO social_posts (`+postCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		p.ID, p.Org, p.Content, p.Channel, p.Status, p.ScheduleAt,
		p.AccountID, p.ExternalID, p.Error, encodeMedia(p.Media),
		p.Origin, p.ExternalIDPostiz, p.ExternalIntegrationId, p.ExternalGroup, p.ReleaseURL,
		p.CreatedAt, p.UpdatedAt)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			if p.Media == nil {
				p.Media = []string{}
			}
			return p, true, nil
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET
			content=$1,channel=$2,status=$3,schedule_at=$4,account_id=$5,external_id=$6,error=$7,
			media=$8,external_group=$9,release_url=$10,updated_at=$11
		 WHERE org=$12 AND origin='postiz' AND external_id_postiz=$13 AND external_integration_id=$14`,
		p.Content, p.Channel, p.Status, p.ScheduleAt,
		p.AccountID, p.ExternalID, p.Error, encodeMedia(p.Media),
		p.ExternalGroup, p.ReleaseURL, p.UpdatedAt,
		p.Org, p.ExternalIDPostiz, p.ExternalIntegrationId); err != nil {
		return socialPost{}, false, fmt.Errorf("upsert mirrored update: %w", err)
	}
	if p.Media == nil {
		p.Media = []string{}
	}
	return p, false, nil
}

func (s *Store) DeletePost(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM social_posts WHERE org=$1 AND id=$2`, org, id)
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
// the scheduler — an UPDATE guarded by the status it expects affects a given row for
// exactly one caller (a file's one writer, or the row lock a second UPDATE waits on
// and then re-reads), so a post is published AT MOST once even if the handler and a
// scheduler tick race for it. Returns (post, true) when THIS caller won the claim, (post,
// false) when the post is not claimable (already published, or being published by
// another caller), and errNotFound if the post is not the org's.
func (s *Store) ClaimForPublish(ctx context.Context, org, id string, now int64) (socialPost, bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='publishing',updated_at=$1 WHERE org=$2 AND id=$3 AND status IN ('draft','scheduled','failed')`,
		now, org, id)
	if err != nil {
		return socialPost{}, false, fmt.Errorf("claim post: %w", err)
	}
	claimed := int64(0)
	if n, _ := res.RowsAffected(); n > 0 {
		claimed = n
	}
	p, err := s.GetPost(ctx, org, id) // errNotFound distinguishes absent from not-claimable
	if err != nil {
		return socialPost{}, false, err
	}
	// Mirrored posts are read-only and are never claimable.
	if p.Origin == "postiz" || p.Status == statusMirrored {
		return p, false, nil
	}
	return p, claimed > 0, nil
}

// MarkPublished records a successful publish: status→published, the first account it
// went through, the provider's external post id, and the note: empty clears any prior
// error, and otherwise names the accounts that did not take the post. Org-scoped.
func (s *Store) MarkPublished(ctx context.Context, org, id, accountID, externalID, note string, now int64) error {
	if err := dialect.Text(accountID, externalID, note); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='published',account_id=$1,external_id=$2,error=$3,updated_at=$4 WHERE org=$5 AND id=$6`,
		accountID, externalID, note, now, org, id); err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}

// MarkFailed records a failed publish: status→failed with an honest reason (retryable
// — a failed post can be claimed again). Org-scoped.
func (s *Store) MarkFailed(ctx context.Context, org, id, reason string, now int64) error {
	if err := dialect.Text(reason); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='failed',error=$1,updated_at=$2 WHERE org=$3 AND id=$4`,
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
		`SELECT org,id FROM social_posts WHERE status='scheduled' AND schedule_at<=$1 ORDER BY schedule_at LIMIT $2`,
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

// RecoverStuckPublishing resets a post left in the transient 'publishing' state by a
// process that died mid-attempt back to 'failed' (retryable), and only a post that has
// been there for stale seconds or more. It is deliberately NOT reset to 'published': we
// cannot know whether the provider actually received the post, and a false 'published'
// would silently drop it, whereas a false 'failed' is a safe, visible retry.
//
// WHY ONLY A STALE ONE. A replica starting (a rolling update's surge pod) runs this
// beside replicas that are publishing; the claim stamps updated_at, and an attempt is
// bounded by publishTimeout, so a post claimed more recently than stale is being
// published by a live process, and failing it would let it be claimed and published a
// second time. Cross-org recovery sweep; returns the count reset.
func (s *Store) RecoverStuckPublishing(ctx context.Context, now, stale int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_posts SET status='failed',error='interrupted before completion',updated_at=$1 WHERE status='publishing' AND updated_at<=$2`,
		now, now-stale)
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
		 FROM social_posts WHERE org=$1`, org)
	if err = row.Scan(&posts, &scheduled, &published); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count posts: %w", err)
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM social_accounts WHERE org=$1`, org).Scan(&accounts); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count accounts: %w", err)
	}
	return posts, scheduled, published, accounts, nil
}

// RecordEvent stores one event keyed by (org, name, sha256(raw body)). The
// dedup unique index makes replay a no-op: inserting the same delivery twice
// writes one row. Returns true when the event was newly recorded, false when
// it was already present.
func (s *Store) RecordEvent(ctx context.Context, org, name string, raw []byte, now int64) (recorded bool, err error) {
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:])
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO social_events (id, org, name, payload, created_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (org, name, payload) DO NOTHING`,
		id, org, name, string(raw), now)
	if err != nil {
		return false, fmt.Errorf("record event: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// EventDedupKey returns the dedup id for a raw body — the sha256 hex digest,
// used so the same delivery never lands twice.
func EventDedupKey(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
