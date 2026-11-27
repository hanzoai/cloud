package provider

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"

	// locus.Open is the ONE opener: on hanzo-sql (CLOUD_SQL_DSN) a schema, otherwise
	// the SQLite file, born encrypted under the key cek derives from the process
	// master and the namespace, with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

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
// sibling of Connection (the /v1/connection plane). The credential itself lives

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

// Store is the integrations database. ONE database — the deployment's own
// "integrations", a SQLite file or a schema on hanzo-sql — holds every org's
// connections + in-flight OAuth nonces; tenancy is the org column (PK includes it).
// Its SQL is the dialect both engines share (sdk/dialect).
type Store struct {
	db     *sql.DB
	fields *tenure.Fields // blinds a nonce, and opens a code a file held sealed before KMS
	cipher *tenure.Cipher // opens the codes a file concealed before the field format; only the migration reads it
	esc    *escrow.Escrow // keeps the grants' codes
}

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "integrations"

// What this store keeps of a secret, on both engines:
//
//	grants.code           the provider's device handle: for GitHub it is exchange-complete on
//	                      its own. A KMS value (internal/escrow), kept at
//	                      orgs/<org>/provider/<grant id>/code@default for the grant's life (at
//	                      most fifteen minutes); the column holds the path and no byte of it.
//	oauth_nonces.blind    the single-use nonce a connect flow carries, and the code a
//	                      Telegram chat redeems, which is a bearer. It is looked up by its value
//	                      and never read back, so the table holds its BLIND index (internal/org,
//	                      field.go), a keyed hash that cannot be turned back into the nonce, and
//	                      not the nonce itself: there is no secret here to keep in KMS.
//
// Left plain: ids, the provider vocabulary, scopes, user_code (the short code shown to the
// person who authorizes the device), timestamps and the return address.

// legacyCodeSubsystem and legacyCodePrefix are what a grant's code is concealed under in a
// file from before the field format. Only the migration reads them.
const (
	legacyCodeSubsystem = "integrations.grant"
	legacyCodePrefix    = "sealed."
)

// nonce is the key a nonce is stored under.
func (s *Store) nonce(value string) string { return s.fields.Blind("oauth_nonces", "nonce", value) }

// openStore opens the store under dir (a file) or on hanzo-sql, with master blinding the
// nonces and opening a code a file held before KMS, and k keeping every code. With no
// master the store does not open, since no nonce can be looked up; with no KMS a code is
// neither kept nor read.
func openStore(dir string, master []byte, k escrow.KMS) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, fields: fields, cipher: c, esc: escrow.New(k, "provider")}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		// A file from before the nonces were blinded keys them by the nonce itself. Set
		// that table aside, make the new one, and carry each row across under its blind.
		var legacy bool
		if !d.Postgres() {
			var err error
			if legacy, err = d.HasColumn(ctx, tx, "oauth_nonces", "nonce"); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			if legacy {
				if err := alignNonces(ctx, d, tx); err != nil {
					return err
				}
				for _, q := range []string{`DROP INDEX IF EXISTS ix_nonces_created`, `ALTER TABLE oauth_nonces RENAME TO oauth_nonces_plain`} {
					if _, err := tx.ExecContext(ctx, q); err != nil {
						return fmt.Errorf("migrate: %w", err)
					}
				}
			}
		}
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if err := alignConnections(ctx, d, tx); err != nil {
			return err
		}
		if legacy {
			if err := s.blindNonces(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.moveToKMS(ctx)
}

// blindNonces carries the rows of the plaintext nonce table into the blinded one and drops
// it. A nonce lives ten minutes, but a flow in flight when the file is upgraded still
// completes.
func (s *Store) blindNonces(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT nonce, org, provider, created_at, back FROM oauth_nonces_plain`)
	if err != nil {
		return fmt.Errorf("migrate nonces: %w", err)
	}
	type held struct {
		nonce, org, provider, back string
		created                    int64
	}
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.nonce, &h.org, &h.provider, &h.created, &h.back); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate nonces: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("migrate nonces: %w", err)
	}
	for _, h := range found {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO oauth_nonces (blind,org,provider,created_at,back) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (blind) DO NOTHING`,
			s.nonce(h.nonce), h.org, h.provider, h.created, h.back); err != nil {
			return fmt.Errorf("migrate nonces: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE oauth_nonces_plain`); err != nil {
		return fmt.Errorf("migrate nonces: %w", err)
	}
	return nil
}

// moveToKMS writes, once, the codes a file holds from before KMS (plain, concealed under
// org.Cipher.Conceal, or sealed as a field) to KMS and replaces each column with its path.
// It runs outside the schema's transaction, because it calls KMS; it compares what it read,
// so another replica that moved the row first leaves nothing to change.
func (s *Store) moveToKMS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, org, code FROM grants WHERE code <> '' AND substr(code, 1, 4) <> 'kms:'`)
	if err != nil {
		return fmt.Errorf("migrate grants: %w", err)
	}
	type held struct{ id, org, code string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.org, &h.code); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate grants: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("migrate grants: %w", err)
	}
	for _, h := range found {
		code := h.code
		if strings.HasPrefix(code, "seal:") {
			r := s.fields.Row("grants", h.org, h.id)
			if code = r.Open("code", code); r.Err() != nil {
				return fmt.Errorf("migrate grants: grant %s: %w", h.id, r.Err())
			}
		} else if code, err = s.unconceal(h.org, code); err != nil {
			return fmt.Errorf("migrate grants: grant %s: %w", h.id, err)
		}
		ref, err := s.esc.Put(ctx, h.org, h.id, "code", code)
		if err != nil {
			return fmt.Errorf("migrate grants: grant %s: %w", h.id, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE grants SET code = $1 WHERE id = $2 AND org = $3 AND code = $4`,
			ref, h.id, h.org, h.code); err != nil {
			return fmt.Errorf("migrate grants: %w", err)
		}
	}
	return nil
}

// unconceal is the plaintext of a code a file holds from before the field format: the
// plain value, or one concealed under its org's key.
func (s *Store) unconceal(orgID, stored string) (string, error) {
	enc, ok := strings.CutPrefix(stored, legacyCodePrefix)
	if !ok {
		return stored, nil
	}
	ns, err := principal.OrgNamespace(orgID, "")
	if err != nil {
		return "", err
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("the concealed code is not a concealed code: %w", err)
	}
	pt, err := s.cipher.Open(ns, legacyCodeSubsystem, b)
	if err != nil {
		return "", fmt.Errorf("open the concealed code: %w", err)
	}
	return string(pt), nil
}

// ddl is the declared schema. "user" is quoted because it is a reserved word on
// PostgreSQL (a bare user there is the session's role, not the column), and every
// integer is BIGINT, which is 64 bits on both engines.
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
  "user"        TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  installer     TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    BIGINT NOT NULL DEFAULT 0,
  connected_at  BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  PRIMARY KEY (org, "user", provider, label)
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

-- blind is the nonce's blind index (internal/org, field.go): the nonce is looked up by
-- its value and never read back, so it is not stored. back is the checked return
-- address the connect named (back.go), '' for the console.
CREATE TABLE IF NOT EXISTS oauth_nonces (
  blind      TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  provider   TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  back       TEXT NOT NULL DEFAULT ''
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
  created_at BIGINT NOT NULL,
  PRIMARY KEY (provider, event_key)
);
CREATE INDEX IF NOT EXISTS ix_bridge_events_created ON bridge_events(created_at);

-- slack_threads are the Slack threads the agent was summoned into (slack_threads.go):
-- a message later said in one reaches the agent without another @mention. Keyed by
-- Slack's own namespace, since a channel id is unique only within its team.
CREATE TABLE IF NOT EXISTS slack_threads (
  team       TEXT NOT NULL,
  channel    TEXT NOT NULL,
  thread     TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (team, channel, thread)
);
CREATE INDEX IF NOT EXISTS ix_slack_threads_created ON slack_threads(created_at);

-- connectors are per-USER links to a provider account (the /v1/connection
-- plane; sibling of the org-scoped connections table). The credential lives
-- ONLY in KMS at /orgs/{org}/users/{user}/connectors/{provider}/{label}; this
-- row is non-secret metadata. label allows multiple accounts per provider;
-- expires_at (unix seconds, 0 = non-expiring) lets the refresh engine decide

-- grants are in-flight device authorizations (poll-until-done, TTL <= 15 min).
-- code is the provider device handle, a secret: it lives in KMS
-- (orgs/<org>/provider/<id>/code), and the column holds its path. The row keeps
-- what KMS does not model: the read-time TTL, the poll cadence and the GC, which
-- forgets the code with the row.
-- NOTE: github-copilot's code is exchange-complete on its own
-- (openai's is not — the server-side code_verifier is never stored), so the
-- code is load-bearing for copilot specifically; the tight TTL
-- bounds the exposure. last_poll_at drives the server-side poll throttle
-- (RFC-8628: never hit the provider faster than interval — the client_ids are
-- shared across all tenants). Terminal outcomes DELETE the row; pending is
-- existence.
CREATE TABLE IF NOT EXISTS grants (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  "user"       TEXT NOT NULL,
  provider     TEXT NOT NULL,
  label        TEXT NOT NULL,
  code         TEXT NOT NULL,
  user_code    TEXT NOT NULL,
  interval     BIGINT NOT NULL,
  last_poll_at BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_grants_expires ON grants(expires_at);
`

// alignNonces adds `back` to an oauth_nonces created before it. CREATE TABLE IF
// NOT EXISTS leaves an existing table as it was, and a nonce row is ten minutes of
// state, so the column is added in place with its default.
func alignNonces(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	has, err := d.HasColumn(ctx, tx, "oauth_nonces", "back")
	if err != nil {
		return fmt.Errorf("align oauth_nonces: %w", err)
	}
	if has {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE oauth_nonces ADD COLUMN back TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("align oauth_nonces: %w", err)
	}
	return nil
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
// That is not theoretical. Production answered GET /v1/provider with HTTP 500
// `no such column: user`, so every connection in the org read as absent: the Slack
// workspace looked disconnected to everything that asks this store even though the
// install was live and its bot was answering, and provider.db had not been
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
// Fast path first: once every declared column is there, every later boot asks the
// catalog once per column and returns. Only a SQLite file made by an older binary
// reaches the rebuild; a database on hanzo-sql is made with the declared table.
func alignConnections(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	want := strings.Split(strings.ReplaceAll(connCols, `"`, ""), ",")

	// Only the columns present in BOTH can be carried; the rest take their default.
	var shared []string
	for _, c := range want {
		has, err := d.HasColumn(ctx, tx, "connections", c)
		if err != nil {
			return fmt.Errorf("align connections: %w", err)
		}
		if has {
			shared = append(shared, c)
		}
	}
	if len(shared) == len(want) {
		return nil
	}
	cols := `"` + strings.Join(shared, `","`) + `"`

	steps := []string{
		connectionsDeclaredDDL,
		`INSERT INTO connections_declared (` + cols + `) SELECT ` + cols + ` FROM connections`,
		`DROP TABLE connections`,
		`ALTER TABLE connections_declared RENAME TO connections`,
		`CREATE INDEX IF NOT EXISTS ix_conn_provider_extid ON connections(provider, external_id)`,
		`CREATE INDEX IF NOT EXISTS ix_conn_org_provider ON connections(org, provider)`,
	}
	for _, q := range steps {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("align connections: %w", err)
		}
	}
	return nil
}

// connectionsDeclaredDDL is the declared table under a build name. It is spelled
// out rather than derived from the DDL above because a rebuild that guessed at the
// shape it was rebuilding TO would be the same class of bug this function fixes.
const connectionsDeclaredDDL = `
CREATE TABLE connections_declared (
  org           TEXT NOT NULL,
  "user"        TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  installer     TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    BIGINT NOT NULL DEFAULT 0,
  connected_at  BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  PRIMARY KEY (org, "user", provider, label)
)`

func (s *Store) Close() error { return s.db.Close() }

// connCols names the columns in the order scanConnection reads them. "user" is quoted
// because it is a reserved word on PostgreSQL.
const connCols = `org,"user",provider,label,external_id,account_label,bot_user_id,installer,scopes_csv,expires_at,connected_at,updated_at`

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
//
// The check and the insert are one transaction. As two statements, orgs racing
// for one account each read "nobody holds it" before any of them had written,
// and all of them bound it.
func (s *Store) write(ctx context.Context, c Connection, shared bool) error {
	if err := dialect.Text(c.Org, c.User, c.Provider, c.Label, c.ExternalID, c.AccountLabel,
		c.BotUserID, c.Installer, encodeScopes(c.Scopes)); err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !shared {
		other, err := holder(ctx, tx, c.Provider, c.ExternalID, c.Org)
		if err != nil {
			return err
		}
		if other != "" {
			return fmt.Errorf("%w: %s %s belongs to %s", errBound, c.Provider, c.ExternalID, other)
		}
	}
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO connections (`+connCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT(org,"user",provider,label) DO UPDATE SET
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	return nil
}

// Holder names an org other than org that holds a provider account, or "" when
// none does. An empty external id is no account, so nobody holds it.
func (s *Store) Holder(ctx context.Context, provider, externalID, org string) (string, error) {
	return holder(ctx, s.db, provider, externalID, org)
}

func holder(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, provider, externalID, org string) (string, error) {
	if strings.TrimSpace(externalID) == "" {
		return "", nil
	}
	var other string
	err := q.QueryRowContext(ctx,
		`SELECT org FROM connections WHERE provider=$1 AND external_id=$2 AND org<>$3 LIMIT 1`,
		provider, externalID, org).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("holder: %w", err)
	}
	return other, nil
}

// Release removes every connection a provider account backs, in every org that
// holds it, and names those orgs. It is how an uninstall the provider reports
// lands: the account is gone for all of them at once, shared or not.
func (s *Store) Release(ctx context.Context, provider, externalID string) ([]string, error) {
	if strings.TrimSpace(externalID) == "" {
		return nil, nil
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT org FROM connections WHERE provider=$1 AND external_id=$2 ORDER BY org`, provider, externalID)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	var orgs []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("release: %w", err)
		}
		orgs = append(orgs, org)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM connections WHERE provider=$1 AND external_id=$2`, provider, externalID); err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	return orgs, nil
}

// Get returns the connection for (org,provider,owner). found=false (nil error)
// when there is no row; a real DB error is returned as err.
//
// The owner is part of the key, not a filter: a provider installed per account
// has one connection per account, and asking without naming one cannot have a
// single right answer once an org holds more than one.
func (s *Store) Get(ctx context.Context, org, user, provider, label string) (Connection, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=$1 AND "user"=$2 AND provider=$3 AND label=$4`, org, user, provider, label)
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
		`SELECT `+connCols+` FROM connections WHERE org=$1 AND ("user"='' OR "user"=$2)
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
		`SELECT `+connCols+` FROM connections WHERE org=$1 AND provider=$2 ORDER BY "user" ASC, label ASC`, org, provider)
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
		`DELETE FROM connections WHERE org=$1 AND "user"=$2 AND provider=$3 AND label=$4`, org, user, provider, label)
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
		`DELETE FROM connections WHERE org=$1 AND "user"=$2 AND provider=$3`, org, user, provider)
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
		`SELECT COUNT(*) FROM connections WHERE org=$1 AND "user"=$2 AND provider=$3`,
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
		`SELECT org FROM connections WHERE provider=$1 AND external_id=$2 ORDER BY connected_at ASC LIMIT 1`,
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
	// One row per (account, org), the accounts that more than one org holds. The orgs
	// of an account are gathered here and not by GROUP_CONCAT: DISTINCT with a
	// separator is not SQLite's, and string_agg is not the same function on both.
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, external_id, org FROM (
		   SELECT provider, external_id, org, COUNT(*) OVER (PARTITION BY provider, external_id) AS held
		   FROM (SELECT DISTINCT provider, external_id, org FROM connections WHERE external_id <> '') AS accounts
		 ) AS counted
		 WHERE held > 1
		 ORDER BY provider, external_id, org`)
	if err != nil {
		return nil, fmt.Errorf("collisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []collision
	for rows.Next() {
		var provider, externalID, org string
		if err := rows.Scan(&provider, &externalID, &org); err != nil {
			return nil, fmt.Errorf("scan collision: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].Provider == provider && out[n-1].ExternalID == externalID {
			out[n-1].Orgs = append(out[n-1].Orgs, org)
			continue
		}
		out = append(out, collision{Provider: provider, ExternalID: externalID, Orgs: []string{org}})
	}
	return out, rows.Err()
}

// PutNonce records a single-use OAuth nonce bound to (org,provider), with the
// checked return address its callback lands on (” for the console). A duplicate
// nonce (astronomically unlikely from 128 bits) is a conflict, surfaced so connect
// fails rather than silently overwriting an in-flight one.
func (s *Store) PutNonce(ctx context.Context, nonce, org, provider, back string) error {
	if err := dialect.Text(nonce, org, provider, back); err != nil {
		return fmt.Errorf("put nonce: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_nonces (blind,org,provider,created_at,back) VALUES ($1,$2,$3,$4,$5)`,
		s.nonce(nonce), org, provider, time.Now().Unix(), back)
	if err != nil {
		return fmt.Errorf("put nonce: %w", err)
	}
	return nil
}

// ConsumeNonce atomically deletes the nonce bound to (org,provider) and answers
// the return address it was stored with. A second consume (replay) or a
// mismatched org/provider deletes nothing → consumed=false. The one DELETE …
// RETURNING IS the single-use proof (no read-then-delete race).
func (s *Store) ConsumeNonce(ctx context.Context, nonce, org, provider string) (back string, consumed bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`DELETE FROM oauth_nonces WHERE blind=$1 AND org=$2 AND provider=$3 RETURNING back`,
		s.nonce(nonce), org, provider).Scan(&back)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("consume nonce: %w", err)
	}
	return back, true, nil
}

// ClaimNonce atomically resolves the org bound to a nonce for `provider` and
// consumes it (single-use), returning the org. Unlike ConsumeNonce it does NOT know
// the org up front — it is the redemption side of a deep-link connect code
// (Telegram): the code arrives in a webhook that has not yet resolved a tenant, so
// the code ITSELF carries the org. found=false (nil error) when the code is
// unknown/already-claimed. The one DELETE ... RETURNING is the single-use proof: of
// any number of claimants racing for one code, on any number of connections, the
// row goes to exactly one.
func (s *Store) ClaimNonce(ctx context.Context, nonce, provider string) (string, bool, error) {
	if strings.TrimSpace(nonce) == "" {
		return "", false, nil
	}
	var org string
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM oauth_nonces WHERE blind=$1 AND provider=$2 RETURNING org`, s.nonce(nonce), provider).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claim nonce: %w", err)
	}
	return org, true, nil
}

// GCNonces deletes nonces created before `before` (unix seconds). Returns how
// many were reaped. Called opportunistically on connect so abandoned flows don't
// GCNonces deletes nonces created before `before` (unix seconds). Returns how
// many were reaped. Called opportunistically on connect so abandoned flows don't
// accrete.
func (s *Store) GCNonces(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oauth_nonces WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("gc nonces: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

const grantCols = `id,org,"user",provider,label,code,user_code,interval,last_poll_at,created_at,expires_at`

// PutGrant stores one in-flight device authorization. The row is written once
// and polled until the provider answers or it expires.
func (s *Store) PutGrant(ctx context.Context, g Grant) error {
	if err := dialect.Text(g.ID, g.Org, g.User, g.Provider, g.Label, g.Code, g.UserCode); err != nil {
		return fmt.Errorf("put grant: %w", err)
	}
	ref, err := s.esc.Put(ctx, g.Org, g.ID, "code", g.Code)
	if err != nil {
		return fmt.Errorf("put grant: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO grants (`+grantCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		g.ID, g.Org, g.User, g.Provider, g.Label, ref, g.UserCode,
		g.Interval, g.LastPollAt, g.CreatedAt, g.ExpiresAt)
	if err != nil {
		_ = s.esc.Drop(ctx, g.Org, ref)
		return fmt.Errorf("put grant: %w", err)
	}
	return nil
}

// GetGrant is (id,org,user)-scoped — another tenant's poll of a known id is
// found=false — and enforces the TTL in SQL (expires_at > now), so an expired
// grant is indistinguishable from an unknown one at read time.
func (s *Store) GetGrant(ctx context.Context, id, org, user string) (Grant, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+grantCols+` FROM grants WHERE id=$1 AND org=$2 AND "user"=$3 AND expires_at > $4`,
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
	if err := s.openGrant(ctx, &g); err != nil {
		return Grant{}, false, fmt.Errorf("get grant: %w", err)
	}
	return g, true, nil
}

// TakeGrant reads one live grant and deletes it in ONE statement, so of any
// number of callers racing for it exactly one gets it. Scoped like GetGrant: an
// id another (org, user) holds is simply not found.
func (s *Store) TakeGrant(ctx context.Context, id, org, user string) (Grant, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM grants WHERE id=$1 AND org=$2 AND "user"=$3 AND expires_at > $4 RETURNING `+grantCols,
		id, org, user, time.Now().Unix())
	var g Grant
	err := row.Scan(&g.ID, &g.Org, &g.User, &g.Provider, &g.Label, &g.Code, &g.UserCode,
		&g.Interval, &g.LastPollAt, &g.CreatedAt, &g.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, false, nil
	}
	if err != nil {
		return Grant{}, false, fmt.Errorf("take grant: %w", err)
	}
	// The row is gone; its code is read, then forgotten, and is the caller's alone.
	ref := g.Code
	if err := s.openGrant(ctx, &g); err != nil {
		return Grant{}, false, fmt.Errorf("take grant: %w", err)
	}
	if err := s.esc.Drop(ctx, g.Org, ref); err != nil {
		return Grant{}, false, fmt.Errorf("take grant: %w", err)
	}
	return g, true, nil
}

// openGrant replaces the path a grant's row holds with the code KMS keeps there. KMS
// refuses a path of another org, so a column carried to another tenant fails here.
func (s *Store) openGrant(ctx context.Context, g *Grant) error {
	code, err := s.esc.Get(ctx, g.Org, g.Code)
	if err != nil {
		return err
	}
	g.Code = code
	return nil
}

// SetGrantInterval records a provider slow_down bump so every later poll honors
// the raised cadence.
func (s *Store) SetGrantInterval(ctx context.Context, id string, sec int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE grants SET interval=$1 WHERE id=$2`, sec, id); err != nil {
		return fmt.Errorf("set grant interval: %w", err)
	}
	return nil
}

// TouchGrant sets last_poll_at — the server-side poll-throttle clock. Tests
// pass 0 to rewind the throttle.
func (s *Store) TouchGrant(ctx context.Context, id string, now int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE grants SET last_poll_at=$1 WHERE id=$2`, now, id); err != nil {
		return fmt.Errorf("touch grant: %w", err)
	}
	return nil
}

// DeleteGrant forgets a grant (terminal poll outcomes; idempotent).
func (s *Store) DeleteGrant(ctx context.Context, id string) error {
	rows, err := s.db.QueryContext(ctx, `DELETE FROM grants WHERE id=$1 RETURNING org, code`, id)
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	refs, err := forgotten(rows)
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	return s.forget(ctx, refs)
}

// grantRef is a deleted grant's org and the path its row held.
type grantRef struct{ org, ref string }

// forgotten reads the org and path of each row a DELETE returned, and closes them.
func forgotten(rows *sql.Rows) ([]grantRef, error) {
	defer func() { _ = rows.Close() }()
	var out []grantRef
	for rows.Next() {
		var r grantRef
		if err := rows.Scan(&r.org, &r.ref); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// forget asks KMS to forget the code of each grant a statement deleted.
func (s *Store) forget(ctx context.Context, refs []grantRef) error {
	var errs []error
	for _, r := range refs {
		errs = append(errs, s.esc.Drop(ctx, r.org, r.ref))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("the grant is gone but KMS still holds its code: %w", err)
	}
	return nil
}

// GCGrants deletes expired grants. Returns how many were reaped. Called
// opportunistically on device start so abandoned flows don't accrete
// (GCNonces parity).
func (s *Store) GCGrants(ctx context.Context, now int64) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `DELETE FROM grants WHERE expires_at <= $1 RETURNING org, code`, now)
	if err != nil {
		return 0, fmt.Errorf("gc grants: %w", err)
	}
	refs, err := forgotten(rows)
	if err != nil {
		return 0, fmt.Errorf("gc grants: %w", err)
	}
	return int64(len(refs)), s.forget(ctx, refs)
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
