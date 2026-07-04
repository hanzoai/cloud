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

// Store is the integrations database. ONE SQLite file
// ({DataDir}/integrations.db) holds every org's connections + in-flight OAuth
// nonces; tenancy is the org column (PK includes it).
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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

-- slack_events is the Slack agent bridge's durable event-dedupe table (see
-- slack_dedupe.go). Created here in migrate() — fail-loud at Mount, one place —
-- so the billed webhook path never runs against a missing table (a lazy first-use
-- ensure could half-init and permanently disable the path).
CREATE TABLE IF NOT EXISTS slack_events (
  event_key  TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_slack_events_created ON slack_events(created_at);
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
