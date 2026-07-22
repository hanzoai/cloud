package link

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" name under both build tags: cgo→mattn+SQLCipher, !cgo→modernc).
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

var errNotFound = errors.New("link: not found")

// Store is the login-manager database. ONE SQLite file ({DataDir}/link.db) holds
// every org's Links; tenancy is the (org, subject) pair. It holds NO metering
// client — it is structurally incapable of charging commerce.
//
// It also owns the account-usage SERIES in the datastore (datastore.go): the Link
// row is an account's latest state, the series is its history. Both are the same
// (org, subject) tenancy, so they live behind one store rather than two seams that
// could drift apart on isolation. dsReady latches the idempotent warehouse DDL —
// on success only, so a datastore still connecting at boot is retried, not
// permanently written off.
type Store struct {
	db *sql.DB

	dsMu    sync.Mutex
	dsReady bool
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
	// The routed-usage counter (routed.go) lives on the SAME connection + tenancy as
	// the links, so a router's per-account usage and the accounts it aggregates can
	// never drift apart on isolation.
	if err := s.migrateRouted(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS links (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  subject    TEXT NOT NULL,
  machine    TEXT NOT NULL,
  host       TEXT NOT NULL DEFAULT '',
  os         TEXT NOT NULL DEFAULT '',
  provider   TEXT NOT NULL,
  account    TEXT NOT NULL DEFAULT '',
  plan       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT 'subscription',
  status     TEXT NOT NULL DEFAULT 'linked',
  last_seen  INTEGER NOT NULL DEFAULT 0,
  usage      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- One Link per (tenant, owner, device, provider, account): the identity a
-- collector upserts against, so a re-report updates in place instead of duping.
CREATE UNIQUE INDEX IF NOT EXISTS ux_links_identity
  ON links(org, subject, machine, provider, account);
-- The list projection: a user's links newest-first within their org.
CREATE INDEX IF NOT EXISTS ix_links_owner ON links(org, subject, updated_at);
-- The device projection: a machine's accounts within a user's scope.
CREATE INDEX IF NOT EXISTS ix_links_device ON links(org, subject, machine);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

const linkCols = `id,org,subject,machine,host,os,provider,account,plan,kind,status,last_seen,usage,created_at,updated_at`

func scanLink(sc interface{ Scan(...any) error }) (Link, error) {
	var x Link
	err := sc.Scan(&x.ID, &x.Org, &x.User, &x.Machine, &x.Host, &x.OS, &x.Provider,
		&x.Account, &x.Plan, &x.Kind, &x.Status, &x.LastSeen, &x.Usage, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

// Upsert registers a Link, keyed by its (org, subject, machine, provider,
// account) identity. A first report INSERTs (using x.ID / x.CreatedAt); a repeat
// UPDATEs in place — bumping last_seen, refreshing the device labels/plan/kind,
// re-activating a revoked account (status→linked), and replacing the usage
// snapshot ONLY when a fresh one is supplied (an empty usage on a heartbeat keeps
// the last good snapshot, mirroring @hanzo/usage's keep-stale-over-flapping rule).
// It returns the stored row (with its authoritative id, which on a repeat is the
// original, not x.ID). Org+subject are part of the identity, so a caller can only
// ever write within their OWN (org, subject) scope.
func (s *Store) Upsert(ctx context.Context, x Link) (Link, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO links (`+linkCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(org, subject, machine, provider, account) DO UPDATE SET
		   host=excluded.host,
		   os=excluded.os,
		   plan=excluded.plan,
		   kind=excluded.kind,
		   status='linked',
		   last_seen=excluded.last_seen,
		   usage=CASE WHEN excluded.usage<>'' THEN excluded.usage ELSE links.usage END,
		   updated_at=excluded.updated_at`,
		x.ID, x.Org, x.User, x.Machine, x.Host, x.OS, x.Provider, x.Account, x.Plan,
		x.Kind, x.Status, x.LastSeen, x.Usage, x.CreatedAt, x.UpdatedAt)
	if err != nil {
		return Link{}, fmt.Errorf("upsert link: %w", err)
	}
	// Read back by identity so the returned row carries the authoritative id
	// (original on a repeat) and the merged usage. Single connection serializes
	// this with the write above.
	return s.getByIdentity(ctx, x.Org, x.User, x.Machine, x.Provider, x.Account)
}

func (s *Store) getByIdentity(ctx context.Context, org, subject, machine, provider, account string) (Link, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+linkCols+` FROM links
		 WHERE org=? AND subject=? AND machine=? AND provider=? AND account=?`,
		org, subject, machine, provider, account)
	x, err := scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, errNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get by identity: %w", err)
	}
	return x, nil
}

// Get returns one Link by id within the caller's (org, subject) scope, or
// errNotFound. The (org, subject, id) triple is the key, so another tenant's or
// another user's id resolves to errNotFound — never a cross-scope read.
func (s *Store) Get(ctx context.Context, org, subject, id string) (Link, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+linkCols+` FROM links WHERE org=? AND subject=? AND id=?`, org, subject, id)
	x, err := scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, errNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("get link: %w", err)
	}
	return x, nil
}

// List returns every Link the (org, subject) owns, most-recently-updated first
// (revoked rows included, so the dashboard can show recent log-outs).
func (s *Store) List(ctx context.Context, org, subject string) ([]Link, error) {
	return s.query(ctx,
		`SELECT `+linkCols+` FROM links WHERE org=? AND subject=? ORDER BY updated_at DESC, id ASC`,
		org, subject)
}

// ListDevice returns the accounts on one machine within the caller's scope,
// most-recently-updated first.
func (s *Store) ListDevice(ctx context.Context, org, subject, machine string) ([]Link, error) {
	return s.query(ctx,
		`SELECT `+linkCols+` FROM links WHERE org=? AND subject=? AND machine=? ORDER BY updated_at DESC, id ASC`,
		org, subject, machine)
}

// ListLinked returns only the ACTIVE (status=linked) accounts the (org, subject)
// owns — the set the route policy considers. Revoked accounts are excluded so a
// logged-out account is never a routing candidate.
func (s *Store) ListLinked(ctx context.Context, org, subject string) ([]Link, error) {
	return s.query(ctx,
		`SELECT `+linkCols+` FROM links WHERE org=? AND subject=? AND status=? ORDER BY updated_at DESC, id ASC`,
		org, subject, StatusLinked)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Link
	for rows.Next() {
		x, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Revoke marks one Link revoked within the caller's scope and returns it (so the
// handler can stop the sessions that ran under that account). ok=false when no
// such Link exists in this (org, subject) — a cross-scope id can neither revoke
// nor probe. An already-revoked Link is returned as-is (idempotent).
func (s *Store) Revoke(ctx context.Context, org, subject, id string, now int64) (Link, bool, error) {
	x, err := s.Get(ctx, org, subject, id)
	if errors.Is(err, errNotFound) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	if x.Status == StatusRevoked {
		return x, true, nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE links SET status=?, updated_at=? WHERE org=? AND subject=? AND id=?`,
		StatusRevoked, now, org, subject, id)
	if err != nil {
		return Link{}, false, fmt.Errorf("revoke link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Link{}, false, nil
	}
	x.Status = StatusRevoked
	x.UpdatedAt = now
	return x, true, nil
}

// RevokeDevice marks EVERY still-linked account on one machine revoked within the
// caller's scope and returns the rows it revoked (so their sessions can be
// stopped). A device with no linked accounts revokes nothing (empty slice).
func (s *Store) RevokeDevice(ctx context.Context, org, subject, machine string, now int64) ([]Link, error) {
	linked, err := s.query(ctx,
		`SELECT `+linkCols+` FROM links WHERE org=? AND subject=? AND machine=? AND status=?`,
		org, subject, machine, StatusLinked)
	if err != nil {
		return nil, err
	}
	if len(linked) == 0 {
		return nil, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE links SET status=?, updated_at=? WHERE org=? AND subject=? AND machine=? AND status=?`,
		StatusRevoked, now, org, subject, machine, StatusLinked); err != nil {
		return nil, fmt.Errorf("revoke device: %w", err)
	}
	for i := range linked {
		linked[i].Status = StatusRevoked
		linked[i].UpdatedAt = now
	}
	return linked, nil
}
