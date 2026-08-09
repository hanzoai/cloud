package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	// sqlpool.Open is the ONE opener: the database is born encrypted under the
	// key cek derives from the process master and the system namespace, and comes
	// back with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sqlpool"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags). Importing modernc
	// directly would double-register and panic at init. Blank import registers
	// the driver — same as clients/agents.
	_ "github.com/hanzoai/sqlite"
)

// Connection is an org's non-secret link to a provider account. The token itself
// is NEVER here — it lives in KMS, keyed by (org,provider); this row holds only
// the metadata needed to render the card and route inbound events.
type Connection struct {
	Org string
	// User is the person this connection belongs to, or "" when the ORG owns it
	// and every member may use it. It is the whole of scope: not a column beside
	// the key but part of it, so the same provider connected both ways is two
	// rows and neither shadows the other.
	User     string
	Provider string
	// Label tells several accounts of one provider apart — a GitHub org login,
	// "" for a provider with a single account.
	Label        string
	ExternalID   string
	AccountLabel string
	BotUserID    string
	// Installer is the provider-side user who completed the install. Whoever
	// finished the OAuth was already an admin of this org, which is why they need
	// no second proof to talk to the bot they installed.
	Installer   string
	Scopes      []string
	ExpiresAt   int64 // access-token expiry, unix seconds; 0 = non-expiring
	ConnectedAt int64
	UpdatedAt   int64
}

// Connection is a user's non-secret link to a provider account — the per-user
// sibling of Connection (the /v1/connectors plane). The credential itself lives

// Grant is one in-flight device authorization. Code/UserCode come from the
// provider; Interval (seconds) is raised by slow_down; LastPollAt gates the
// server-side poll throttle; ExpiresAt is enforced at read time, not only GC.
type Grant struct {
	ID, Org, User, Provider, Label string
	Code, UserCode                 string
	Interval                       int64 // seconds between polls
	LastPollAt                     int64 // unix seconds of the last upstream poll; 0 = never
	CreatedAt, ExpiresAt           int64 // unix seconds
}

// Store is the integrations database. ONE SQLite file — the deployment's own
// "integrations" — holds every org's connections + in-flight OAuth nonces;
// tenancy is the org column (PK includes it).
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	db, err := sqlpool.Open("integrations", dir)
	if err != nil {
		return nil, err
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
-- One row per (org, provider, owner). The owner is the provider-side account a
-- connection is FOR: a GitHub App is installed per account, so one org holding
-- several GitHub organizations keeps one row each and mints a separate
-- installation token per row. A provider with one account carries label=''.
--
-- user='' means the ORG owns this connection and every member may use it;
-- otherwise it is that person's alone. That is the whole of scope — not a
-- column, a convention, and the same one label='' already carried for accounts.
-- A provider may be connected BOTH ways at once: two rows, neither shadowing
-- the other, which is what "connect Google for the org or just me" asks for.
CREATE TABLE IF NOT EXISTS connections (
  org           TEXT NOT NULL,
  user          TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  installer     TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    INTEGER NOT NULL DEFAULT 0,
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, user, provider, label)
);
-- Plain, not UNIQUE, and that is deliberate. One provider account belongs to one
-- org (Upsert enforces it), but the constraint SQL can express here is not the
-- one we mean: UNIQUE(provider, external_id) collides on every row that carries
-- no external id, and it also forbids the same org holding an account twice —
-- once for the org, once for a person — which the user column exists to allow.
-- The predicate is "no two DISTINCT orgs", which no index states, so it lives in
-- Upsert, the one statement every write passes through, and Collisions reports
-- the rows that predate it.
CREATE INDEX IF NOT EXISTS ix_conn_provider_extid ON connections(provider, external_id);
CREATE INDEX IF NOT EXISTS ix_conn_org_provider ON connections(org, provider);

CREATE TABLE IF NOT EXISTS oauth_nonces (
  nonce      TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  provider   TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_nonces_created ON oauth_nonces(created_at);

-- bridge_events is the ChatBridge's durable, provider-keyed event-dedupe table
-- (see bridge_dedupe.go) shared by EVERY chat platform (Slack/Teams/Discord/Telegram).
-- Created here in migrate() — fail-loud at Mount, one place — so the billed webhook
-- path never runs against a missing table (a lazy first-use ensure could half-init
-- and permanently disable the path). PK is (provider, event_key) so platform id
-- spaces never collide.
CREATE TABLE IF NOT EXISTS bridge_events (
  provider   TEXT NOT NULL,
  event_key  TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (provider, event_key)
);
CREATE INDEX IF NOT EXISTS ix_bridge_events_created ON bridge_events(created_at);

-- connectors are per-USER links to a provider account (the /v1/connectors
-- plane; sibling of the org-scoped connections table). The credential lives
-- ONLY in KMS at /orgs/{org}/users/{user}/connectors/{provider}/{label}; this
-- row is non-secret metadata. label allows multiple accounts per provider;
-- expires_at (unix seconds, 0 = non-expiring) lets the refresh engine decide

-- grants are in-flight device authorizations (poll-until-done, TTL <= 15 min).
-- code is the provider device handle (secret-adjacent): it lives HERE, not in
-- KMS, because this store is encrypted at rest (its key is derived from the
-- process master and this database's name; a store with no master does not
-- open) and the row needs non-destructive polling reads, read-time TTL, poll
-- cadence, and GC — a lifecycle KMS does not model. It shares the at-rest
-- posture of oauth_nonces (same class of short-lived material).
-- NOTE: github-copilot's code is exchange-complete on its own
-- (openai's is not — the server-side code_verifier is never stored), so the
-- at-rest posture is load-bearing for copilot specifically; the tight TTL
-- bounds the exposure. last_poll_at drives the server-side poll throttle
-- (RFC-8628: never hit the provider faster than interval — the client_ids are
-- shared across all tenants). Terminal outcomes DELETE the row; pending is
-- existence.
CREATE TABLE IF NOT EXISTS grants (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  user         TEXT NOT NULL,
  provider     TEXT NOT NULL,
  label        TEXT NOT NULL,
  code         TEXT NOT NULL,
  user_code    TEXT NOT NULL,
  interval     INTEGER NOT NULL,
  last_poll_at INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_grants_expires ON grants(expires_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return s.alignConnections()
}

// alignConnections rebuilds `connections` when the table on disk is not the table
// declared above.
//
// It has to exist because `CREATE TABLE IF NOT EXISTS` is a NO-OP on a database
// that already has the table. Every column this schema has gained — user, label,
// expires_at — and the primary key it moved to (org,user,provider,label) therefore
// reached new databases only. An existing one kept whatever shape it was created
// with, forever, while the code went on selecting the new columns.
//
// That is not theoretical. Production answered GET /v1/integrations with HTTP 500
// `no such column: user`, so every connection in the org read as absent: the Slack
// workspace looked disconnected to everything that asks this store even though the
// install was live and its bot was answering, and integrations.db had not been
// written to in twelve days. A fresh deployment was perfect and every real one was
// broken, which is the signature of a declared schema with no migration behind it.
//
// The rebuild carries rows across by the INTERSECTION of the old and new column
// lists, so it does not need to know which historical shape it is coming from — a
// column the old table never had takes its declared default, and ” is exactly
// what an empty user or label means (a connection the ORG holds rather than a
// person). Anything the old table had and this one does not is dropped, because
// the declared schema is the schema.
//
// Fast path first: once the shapes match, every later boot does one pragma read
// and returns.
func (s *Store) alignConnections() error {
	have, err := s.columns("connections")
	if err != nil {
		return err
	}
	want := strings.Split(connCols, ",")
	if sameColumns(have, want) {
		return nil
	}

	// Only the columns present in BOTH can be carried; the rest take their default.
	var shared []string
	for _, c := range want {
		if slices.Contains(have, c) {
			shared = append(shared, c)
		}
	}
	cols := strings.Join(shared, ",")

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("align connections: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	steps := []string{
		connectionsDeclaredDDL,
		`INSERT INTO connections_declared (` + cols + `) SELECT ` + cols + ` FROM connections`,
		`DROP TABLE connections`,
		`ALTER TABLE connections_declared RENAME TO connections`,
		`CREATE INDEX IF NOT EXISTS ix_conn_provider_extid ON connections(provider, external_id)`,
		`CREATE INDEX IF NOT EXISTS ix_conn_org_provider ON connections(org, provider)`,
	}
	for _, q := range steps {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("align connections: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("align connections: commit: %w", err)
	}
	return nil
}

// connectionsDeclaredDDL is the declared table under a build name. It is spelled
// out rather than derived from the DDL above because a rebuild that guessed at the
// shape it was rebuilding TO would be the same class of bug this function fixes.
const connectionsDeclaredDDL = `
CREATE TABLE connections_declared (
  org           TEXT NOT NULL,
  user          TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  installer     TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    INTEGER NOT NULL DEFAULT 0,
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, user, provider, label)
)`

// columns reports the column names a table actually has, in declaration order.
func (s *Store) columns(table string) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan %s column: %w", table, err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// sameColumns compares two column sets as SETS: order is a property of how a
// table was written, not of whether it holds what the code reads.
func sameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func (s *Store) Close() error { return s.db.Close() }

const connCols = `org,user,provider,label,external_id,account_label,bot_user_id,installer,scopes_csv,expires_at,connected_at,updated_at`

func scanConnection(sc interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var scopes string
	err := sc.Scan(&c.Org, &c.User, &c.Provider, &c.Label, &c.ExternalID, &c.AccountLabel,
		&c.BotUserID, &c.Installer, &scopes, &c.ExpiresAt, &c.ConnectedAt, &c.UpdatedAt)
	c.Scopes = decodeScopes(scopes)
	return c, err
}

// errBound reports a write that would bind a provider account a DIFFERENT org
// already holds. Callers that answer a tenant map it to a flat "persist failed"
// and log the detail — the org named in the message is another tenant's.
var errBound = errors.New("provider account is bound to another org")

// Upsert stores (or refreshes) a connection. On a re-connect the original
// connected_at is PRESERVED ("connected since"), only updated_at advances.
//
// A write whose external_id another org holds is REFUSED. The external id IS the
// provider-side account — a GitHub App installation, a Slack team — and one
// account belongs to one tenant. The key (org,user,provider,label) cannot say
// that: a second org binding the same account writes a perfectly valid row, and
// nothing downstream can tell it from a real one. What follows is not a subtler
// tenancy question but one tenant's credentials answering for another —
// ResolveOrgByExternalID hands that account's inbound events to whichever org
// connected first, and every token minted through the second row is minted
// against the first org's account.
//
// The predicate is "a different org", not "another row", because the same org may
// hold an account twice: once for the org and once for a person. That is the user
// column doing its job and it stays legal.
func (s *Store) Upsert(ctx context.Context, c Connection) error {
	return s.write(ctx, c, false)
}

// Share stores a connection that a DIFFERENT org may already hold — the same
// provider account reachable from more than one of our own orgs.
//
// It exists because "one account belongs to one tenant" is the right default and
// the wrong absolute. The GitHub org `luxfi` is developed from the `hanzo` org and
// is also Lux's own; binding it once means one of those two contexts cannot see its
// own repositories. So the same installation may be held by several orgs.
//
// It is a SEPARATE FUNCTION rather than a flag on Upsert, and that is the whole
// safety argument. The App here is Hanzo's own, so a customer installs it on THEIR
// GitHub org — and if exclusivity were merely a parameter, any caller that passed
// the wrong value would let one tenant claim another tenant's installation and read
// their private repositories. A caller reaches this by naming it, so the one call
// site that may is visible, and everything else keeps the refusal by construction.
//
// The only caller is the platform-sudo claim path. A tenant's self-service connect
// goes through Upsert and still cannot take an account another org holds.
func (s *Store) Share(ctx context.Context, c Connection) error {
	return s.write(ctx, c, true)
}

// write is the ONE statement every connection passes through; shared says whether
// an account another org holds is permitted.
func (s *Store) write(ctx context.Context, c Connection, shared bool) error {
	if !shared && strings.TrimSpace(c.ExternalID) != "" {
		var other string
		err := s.db.QueryRowContext(ctx,
			`SELECT org FROM connections WHERE provider=? AND external_id=? AND org<>? LIMIT 1`,
			c.Provider, c.ExternalID, c.Org).Scan(&other)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s %s belongs to %s", errBound, c.Provider, c.ExternalID, other)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("upsert connection: %w", err)
		}
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connections (`+connCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,user,provider,label) DO UPDATE SET
		   external_id=excluded.external_id,
		   account_label=excluded.account_label,
		   bot_user_id=excluded.bot_user_id,
		   -- Kept when a re-connect does not carry one, so re-authorising with a
		   -- narrower grant cannot erase who installed this.
		   installer=CASE WHEN excluded.installer='' THEN connections.installer ELSE excluded.installer END,
		   scopes_csv=excluded.scopes_csv,
		   expires_at=excluded.expires_at,
		   updated_at=excluded.updated_at`,
		c.Org, c.User, c.Provider, c.Label, c.ExternalID, c.AccountLabel, c.BotUserID, c.Installer,
		encodeScopes(c.Scopes), c.ExpiresAt, now, now)
	if err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	return nil
}

// Get returns the connection for (org,provider,owner). found=false (nil error)
// when there is no row; a real DB error is returned as err.
//
// The owner is part of the key, not a filter: a provider installed per account
// has one connection per account, and asking without naming one cannot have a
// single right answer once an org holds more than one.
func (s *Store) Get(ctx context.Context, org, user, provider, label string) (Connection, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=? AND user=? AND provider=? AND label=?`, org, user, provider, label)
	c, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, false, nil
	}
	if err != nil {
		return Connection{}, false, fmt.Errorf("get connection: %w", err)
	}
	return c, true, nil
}

// List returns every connection for org, newest-connected first.
func (s *Store) List(ctx context.Context, org, user string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=? AND (user='' OR user=?)
		 ORDER BY connected_at DESC, provider ASC`, org, user)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListFor returns every connection an org holds for one provider — one per
// owner. This is what a caller iterates when it must reach all of an org's
// accounts, such as listing the repositories of every connected GitHub org.
func (s *Store) ListFor(ctx context.Context, org, provider string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=? AND provider=? ORDER BY user ASC, label ASC`, org, provider)
	if err != nil {
		return nil, fmt.Errorf("list connections for provider: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes ONE account: the row this principal holds for a provider under
// a given label. Reports whether a row went, so a caller may repeat itself.
//
// Removing every account for a provider is Disconnect, which is why this takes a
// label and that one does not.
func (s *Store) Delete(ctx context.Context, org, user, provider, label string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM connections WHERE org=? AND user=? AND provider=? AND label=?`, org, user, provider, label)
	if err != nil {
		return false, fmt.Errorf("delete connection: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Disconnect removes EVERY account this principal holds for one provider.
//
// Distinct from Delete on purpose: disconnecting GitHub means all of it, and an
// org that installed the app on four accounts expects one action to end four
// rows. Delete names one account; a caller that means "all" says so rather than
// passing a label that happens to be empty.
func (s *Store) Disconnect(ctx context.Context, org, user, provider string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM connections WHERE org=? AND user=? AND provider=?`, org, user, provider)
	if err != nil {
		return false, fmt.Errorf("disconnect: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Count reports how many connections a principal holds for one provider — what
// a caller asks before minting another label, so a per-provider limit is decided
// against the same rows the reads use.
func (s *Store) Count(ctx context.Context, org, user, provider string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM connections WHERE org=? AND user=? AND provider=?`,
		org, user, provider).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count connections: %w", err)
	}
	return n, nil
}

// ResolveOrgByExternalID maps a provider account id back to the connecting org.
// An empty externalID never matches (so unset/scaffold connections don't collide
// on ""). Ambiguity (two orgs, same external id — should not happen) resolves to
// the earliest-connected deterministically.
func (s *Store) ResolveOrgByExternalID(ctx context.Context, provider, externalID string) (string, bool, error) {
	if strings.TrimSpace(externalID) == "" {
		return "", false, nil
	}
	var org string
	err := s.db.QueryRowContext(ctx,
		`SELECT org FROM connections WHERE provider=? AND external_id=? ORDER BY connected_at ASC LIMIT 1`,
		provider, externalID).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve org by external id: %w", err)
	}
	return org, true, nil
}

// collision is one provider account held by more than one org.
type collision struct {
	Provider   string
	ExternalID string
	Orgs       []string
}

// Collisions reports provider accounts held by more than one org.
//
// TWO DIFFERENT THINGS LOOK IDENTICAL HERE, and this query cannot tell them apart:
//
//   - a DELIBERATE share. Share writes exactly this shape, because one installation
//     legitimately serves more than one of our orgs — luxfi is developed from the
//     hanzo org and is also Lux's own. That is a platform-sudo decision, and the
//     expected steady state for those accounts.
//   - a LEGACY cross-tenant bind, from before Upsert refused to write one.
//
// So a hit is a QUESTION, not a fault. It was described here as history, which was
// true until sharing existed and is now wrong for the accounts a person shared on
// purpose. Reported, never repaired and never fatal: which org should hold an
// account is not a question this process can answer, and refusing to boot over rows
// already written would take the whole surface down to protect them.
func (s *Store) Collisions(ctx context.Context) ([]collision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, external_id, GROUP_CONCAT(DISTINCT org) FROM connections
		 WHERE external_id != ''
		 GROUP BY provider, external_id
		 HAVING COUNT(DISTINCT org) > 1
		 ORDER BY provider, external_id`)
	if err != nil {
		return nil, fmt.Errorf("collisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []collision
	for rows.Next() {
		var c collision
		var orgs string
		if err := rows.Scan(&c.Provider, &c.ExternalID, &orgs); err != nil {
			return nil, fmt.Errorf("scan collision: %w", err)
		}
		c.Orgs = strings.Split(orgs, ",")
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutNonce records a single-use OAuth nonce bound to (org,provider). A duplicate
// nonce (astronomically unlikely from 128 bits) is a conflict, surfaced so connect
// fails rather than silently overwriting an in-flight one.
func (s *Store) PutNonce(ctx context.Context, nonce, org, provider string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_nonces (nonce,org,provider,created_at) VALUES (?,?,?,?)`,
		nonce, org, provider, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("put nonce: %w", err)
	}
	return nil
}

// ConsumeNonce atomically deletes the nonce bound to (org,provider) and reports
// whether exactly one row went. A second consume (replay) or a mismatched
// org/provider deletes zero rows → consumed=false. This single-DELETE-with-rows-
// affected IS the single-use proof (no read-then-delete race).
func (s *Store) ConsumeNonce(ctx context.Context, nonce, org, provider string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_nonces WHERE nonce=? AND org=? AND provider=?`, nonce, org, provider)
	if err != nil {
		return false, fmt.Errorf("consume nonce: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClaimNonce atomically resolves the org bound to a nonce for `provider` and
// consumes it (single-use), returning the org. Unlike ConsumeNonce it does NOT know
// the org up front — it is the redemption side of a deep-link connect code
// (Telegram): the code arrives in a webhook that has not yet resolved a tenant, so
// the code ITSELF carries the org. found=false (nil error) when the code is
// unknown/already-claimed. The Store opens with MaxOpenConns(1) (store.go), so this
// read-then-delete pair runs with no concurrent writer — the DELETE's RowsAffected
// is the single-use proof.
func (s *Store) ClaimNonce(ctx context.Context, nonce, provider string) (string, bool, error) {
	if strings.TrimSpace(nonce) == "" {
		return "", false, nil
	}
	var org string
	err := s.db.QueryRowContext(ctx,
		`SELECT org FROM oauth_nonces WHERE nonce=? AND provider=?`, nonce, provider).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claim nonce: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_nonces WHERE nonce=? AND provider=? AND org=?`, nonce, provider, org)
	if err != nil {
		return "", false, fmt.Errorf("claim nonce delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", false, nil // lost the race to a concurrent claim
	}
	return org, true, nil
}

// GCNonces deletes nonces created before `before` (unix seconds). Returns how
// many were reaped. Called opportunistically on connect so abandoned flows don't
// GCNonces deletes nonces created before `before` (unix seconds). Returns how
// many were reaped. Called opportunistically on connect so abandoned flows don't
// accrete.
func (s *Store) GCNonces(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oauth_nonces WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("gc nonces: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

const grantCols = `id,org,user,provider,label,code,user_code,interval,last_poll_at,created_at,expires_at`

// PutGrant stores one in-flight device authorization. The row is written once
// and polled until the provider answers or it expires.
func (s *Store) PutGrant(ctx context.Context, g Grant) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO grants (`+grantCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		g.ID, g.Org, g.User, g.Provider, g.Label, g.Code, g.UserCode,
		g.Interval, g.LastPollAt, g.CreatedAt, g.ExpiresAt)
	if err != nil {
		return fmt.Errorf("put grant: %w", err)
	}
	return nil
}

// GetGrant is (id,org,user)-scoped — another tenant's poll of a known id is
// found=false — and enforces the TTL in SQL (expires_at > now), so an expired
// grant is indistinguishable from an unknown one at read time.
func (s *Store) GetGrant(ctx context.Context, id, org, user string) (Grant, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+grantCols+` FROM grants WHERE id=? AND org=? AND user=? AND expires_at > ?`,
		id, org, user, time.Now().Unix())
	var g Grant
	err := row.Scan(&g.ID, &g.Org, &g.User, &g.Provider, &g.Label, &g.Code, &g.UserCode,
		&g.Interval, &g.LastPollAt, &g.CreatedAt, &g.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, false, nil
	}
	if err != nil {
		return Grant{}, false, fmt.Errorf("get grant: %w", err)
	}
	return g, true, nil
}

// SetGrantInterval records a provider slow_down bump so every later poll honors
// the raised cadence.
func (s *Store) SetGrantInterval(ctx context.Context, id string, sec int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE grants SET interval=? WHERE id=?`, sec, id); err != nil {
		return fmt.Errorf("set grant interval: %w", err)
	}
	return nil
}

// TouchGrant sets last_poll_at — the server-side poll-throttle clock. Tests
// pass 0 to rewind the throttle.
func (s *Store) TouchGrant(ctx context.Context, id string, now int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE grants SET last_poll_at=? WHERE id=?`, now, id); err != nil {
		return fmt.Errorf("touch grant: %w", err)
	}
	return nil
}

// DeleteGrant forgets a grant (terminal poll outcomes; idempotent).
func (s *Store) DeleteGrant(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	return nil
}

// GCGrants deletes expired grants. Returns how many were reaped. Called
// opportunistically on device start so abandoned flows don't accrete
// (GCNonces parity).
func (s *Store) GCGrants(ctx context.Context, now int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("gc grants: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

// staleNonceCutoff is the GC horizon: a nonce older than the state TTL can never
// verify (its state has expired), so it is safe to reap.
func staleNonceCutoff() int64 { return time.Now().Add(-stateTTL).Unix() }

// encodeScopes / decodeScopes store scopes as a comma-separated string. Scope
// tokens never contain a comma (OAuth scope grammar is space/comma-delimited),
// so CSV is unambiguous and avoids a JSON blob for a flat list.
func encodeScopes(xs []string) string {
	clean := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			clean = append(clean, x)
		}
	}
	return strings.Join(clean, ",")
}

func decodeScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// rfc3339 renders a unix timestamp as RFC3339 UTC (empty for 0).
func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
