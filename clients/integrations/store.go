package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags: cgo → mattn+SQLCipher;
	// !cgo → pure-Go modernc). Importing modernc directly would double-register and
	// panic at init. Blank import registers the driver — same as clients/agents.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Connection is an org's non-secret link to a provider account. The token itself
// is NEVER here — it lives in KMS, keyed by (org,provider); this row holds only
// the metadata needed to render the card and route inbound events.
type Connection struct {
	Org          string
	Provider     string
	ExternalID   string
	AccountLabel string
	BotUserID    string
	Scopes       []string
	ConnectedAt  int64
	UpdatedAt    int64
}

// Connector is a user's non-secret link to a provider account — the per-user
// sibling of Connection (the /v1/connectors plane). The credential itself lives
// ONLY in KMS at userPath(org,user,provider,label); this row holds metadata.
type Connector struct {
	Org, User, Provider, Label string
	ExternalID, AccountLabel   string
	Scopes                     []string
	ExpiresAt                  int64 // access-token expiry, unix seconds; 0 = non-expiring
	ConnectedAt, UpdatedAt     int64
}

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

// Store is the integrations database. ONE SQLite file
// ({DataDir}/integrations.db) holds every org's connections + in-flight OAuth
// nonces; tenancy is the org column (PK includes it).
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS connections (
  org           TEXT NOT NULL,
  provider      TEXT NOT NULL,
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, provider)
);
CREATE INDEX IF NOT EXISTS ix_conn_provider_extid ON connections(provider, external_id);

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
-- without touching KMS.
CREATE TABLE IF NOT EXISTS connectors (
  org           TEXT NOT NULL,
  user          TEXT NOT NULL,
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL,
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  expires_at    INTEGER NOT NULL DEFAULT 0,
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, user, provider, label)
);

-- grants are in-flight device authorizations (poll-until-done, TTL <= 15 min).
-- code is the provider device handle (secret-adjacent): it lives HERE, not in
-- KMS, because this store is cek-encrypted at rest (SQLCipher DEK wrapped
-- under the same master key as KMS; cek.Open fail-secure) and the row needs
-- non-destructive polling reads, read-time TTL, poll cadence, and GC — a
-- lifecycle KMS does not model. Pure-Go dev builds share the existing at-rest
-- posture of oauth_nonces (same class of short-lived material; production is
-- SQLCipher). NOTE: github-copilot's code is exchange-complete on its own
-- (openai's is not — the server-side code_verifier is never stored), so the
-- SQLCipher posture is load-bearing for copilot specifically; the tight TTL
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

-- extension_config is the CONFIG half of the unified /v1/connectors plane: the
-- enablement bit + an opaque per-extension config blob, keyed by the extension
-- identity (scope,org,user,provider,label). Orthogonal to custody — KMS holds the
-- credential, this holds "is it on" + "how is it tuned". A MISSING row means
-- enabled-by-default (every already-connected connector keeps working with no
-- row). org scope → user='' label=''; user scope → user=<id> label=<label>.
-- Tenancy is the (org, and scope='org' OR user) predicate (see ListExtConfig).
CREATE TABLE IF NOT EXISTS extension_config (
  scope      TEXT NOT NULL,
  org        TEXT NOT NULL,
  user       TEXT NOT NULL DEFAULT '',
  provider   TEXT NOT NULL,
  label      TEXT NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  config     TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, org, user, provider, label)
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

const connCols = `org,provider,external_id,account_label,bot_user_id,scopes_csv,connected_at,updated_at`

func scanConnection(sc interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var scopes string
	err := sc.Scan(&c.Org, &c.Provider, &c.ExternalID, &c.AccountLabel, &c.BotUserID,
		&scopes, &c.ConnectedAt, &c.UpdatedAt)
	c.Scopes = decodeScopes(scopes)
	return c, err
}

// Upsert stores (or refreshes) a connection. On a re-connect the original
// connected_at is PRESERVED ("connected since"), only updated_at advances.
func (s *Store) Upsert(ctx context.Context, c Connection) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connections (`+connCols+`) VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,provider) DO UPDATE SET
		   external_id=excluded.external_id,
		   account_label=excluded.account_label,
		   bot_user_id=excluded.bot_user_id,
		   scopes_csv=excluded.scopes_csv,
		   updated_at=excluded.updated_at`,
		c.Org, c.Provider, c.ExternalID, c.AccountLabel, c.BotUserID,
		encodeScopes(c.Scopes), now, now)
	if err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	return nil
}

// Get returns the connection for (org,provider). found=false (nil error) when
// there is no row; a real DB error is returned as err.
func (s *Store) Get(ctx context.Context, org, provider string) (Connection, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=? AND provider=?`, org, provider)
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
func (s *Store) List(ctx context.Context, org string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+connCols+` FROM connections WHERE org=? ORDER BY connected_at DESC, provider ASC`, org)
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

// Delete removes a connection. Reports whether a row went (idempotent caller).
func (s *Store) Delete(ctx context.Context, org, provider string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE org=? AND provider=?`, org, provider)
	if err != nil {
		return false, fmt.Errorf("delete connection: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
// accrete.
func (s *Store) GCNonces(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oauth_nonces WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("gc nonces: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ── connectors (per-user plane) ────────────────────────────────────────────────

const connectorCols = `org,user,provider,label,external_id,account_label,scopes_csv,expires_at,connected_at,updated_at`

func scanConnector(sc interface{ Scan(...any) error }) (Connector, error) {
	var c Connector
	var scopes string
	err := sc.Scan(&c.Org, &c.User, &c.Provider, &c.Label, &c.ExternalID, &c.AccountLabel,
		&scopes, &c.ExpiresAt, &c.ConnectedAt, &c.UpdatedAt)
	c.Scopes = decodeScopes(scopes)
	return c, err
}

// UpsertConnector stores (or refreshes) a connector. On a re-connect or token
// refresh the original connected_at is PRESERVED ("connected since"), only the
// metadata and updated_at advance — Upsert (connections) parity.
func (s *Store) UpsertConnector(ctx context.Context, c Connector) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connectors (`+connectorCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,user,provider,label) DO UPDATE SET
		   external_id=excluded.external_id,
		   account_label=excluded.account_label,
		   scopes_csv=excluded.scopes_csv,
		   expires_at=excluded.expires_at,
		   updated_at=excluded.updated_at`,
		c.Org, c.User, c.Provider, c.Label, c.ExternalID, c.AccountLabel,
		encodeScopes(c.Scopes), c.ExpiresAt, now, now)
	if err != nil {
		return fmt.Errorf("upsert connector: %w", err)
	}
	return nil
}

// GetConnector returns the connector for (org,user,provider,label). found=false
// (nil error) when there is no row.
func (s *Store) GetConnector(ctx context.Context, org, user, provider, label string) (Connector, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+connectorCols+` FROM connectors WHERE org=? AND user=? AND provider=? AND label=?`,
		org, user, provider, label)
	c, err := scanConnector(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connector{}, false, nil
	}
	if err != nil {
		return Connector{}, false, fmt.Errorf("get connector: %w", err)
	}
	return c, true, nil
}

// ListConnectors returns every connector for (org,user), newest-connected first,
// with a deterministic (provider,label) tiebreak.
func (s *Store) ListConnectors(ctx context.Context, org, user string) ([]Connector, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+connectorCols+` FROM connectors WHERE org=? AND user=?
		 ORDER BY connected_at DESC, provider ASC, label ASC`, org, user)
	if err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connector: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountConnectors counts (org,user,provider) rows — the maxConnectors intake cap.
func (s *Store) CountConnectors(ctx context.Context, org, user, provider string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM connectors WHERE org=? AND user=? AND provider=?`,
		org, user, provider).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count connectors: %w", err)
	}
	return n, nil
}

// DeleteConnector removes a connector. Reports whether a row went (idempotent caller).
func (s *Store) DeleteConnector(ctx context.Context, org, user, provider, label string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM connectors WHERE org=? AND user=? AND provider=? AND label=?`,
		org, user, provider, label)
	if err != nil {
		return false, fmt.Errorf("delete connector: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── grants (in-flight device authorizations) ───────────────────────────────────

const grantCols = `id,org,user,provider,label,code,user_code,interval,last_poll_at,created_at,expires_at`

// PutGrant records a started device authorization. A duplicate 128-bit id
// (astronomically unlikely) is a conflict, surfaced so the flow fails rather
// than silently overwriting an in-flight one (PutNonce parity).
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
