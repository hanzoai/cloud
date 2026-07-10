package ingress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags: cgo → mattn+SQLCipher;
	// !cgo → pure-Go modernc). Same driver clients/settings and clients/eval use.
	"github.com/hanzoai/cloud/internal/cek"
	_ "github.com/hanzoai/sqlite"
)

// The ingress STORE is one SQLite file ({DataDir}/ingress/ingress.db) holding
// every org's edge config as opaque JSON documents keyed by (org, kind, id).
// Tenancy is the org column: every CRUD query binds `WHERE org=?`, so one org can
// never read or overwrite another's config. Route HOST, however, is a globally
// unique DNS claim — the store enforces host uniqueness ACROSS orgs (a partial
// unique index) so no org can hijack another's host. The edge COMPILE reads the
// UNION across orgs (All*), which is unambiguous precisely because hosts are
// unique.

// Object is one persisted config object as an opaque JSON doc.
type Object struct {
	ID  string
	Doc string
}

// Store is the ingress metastore over one SQLite file. MaxOpenConns(1)
// serializes writes against the file lock (same discipline as settings/eval).
type Store struct {
	db *sql.DB
}

// ErrHostTaken is returned when a route Put would collide on the globally-unique
// host — another route (in any org) already claims it.
var ErrHostTaken = errors.New("host already claimed by another route")

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
CREATE TABLE IF NOT EXISTS ingress_objects (
  org        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  id         TEXT NOT NULL,
  doc        TEXT NOT NULL,
  host       TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, kind, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ingress_route_host_uniq
  ON ingress_objects(host) WHERE kind='route' AND host<>'';
CREATE TABLE IF NOT EXISTS ingress_tls (
  org        TEXT PRIMARY KEY,
  doc        TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Put upserts an object for (org, kind, id). host is the denormalized route host
// used for the global-uniqueness index ("" for non-route kinds). A collision on
// another route's host returns ErrHostTaken.
func (s *Store) Put(ctx context.Context, org, kind, id, doc, host string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ingress_objects (org,kind,id,doc,host,updated_at) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org,kind,id) DO UPDATE SET doc=excluded.doc, host=excluded.host, updated_at=excluded.updated_at`,
		org, kind, id, doc, host, now)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrHostTaken
		}
		return fmt.Errorf("put %s/%s: %w", kind, id, err)
	}
	return nil
}

// Get returns the object doc for (org, kind, id). found=false (nil error) when
// there is no row. Tenant isolation: the WHERE always binds org.
func (s *Store) Get(ctx context.Context, org, kind, id string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT doc FROM ingress_objects WHERE org=? AND kind=? AND id=?`, org, kind, id)
	var doc string
	err := row.Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get %s/%s: %w", kind, id, err)
	}
	return doc, true, nil
}

// List returns every object of kind for org (tenant-scoped read).
func (s *Store) List(ctx context.Context, org, kind string) ([]Object, error) {
	return s.query(ctx, `SELECT id, doc FROM ingress_objects WHERE org=? AND kind=? ORDER BY id`, org, kind)
}

// All returns every object of kind across ALL orgs — the input to the edge
// compile. It is a server-internal full-table read (NOT a tenant-scoped API
// call); the API handlers use List, which binds org.
func (s *Store) All(ctx context.Context, kind string) ([]Object, error) {
	return s.query(ctx, `SELECT id, doc FROM ingress_objects WHERE kind=? ORDER BY org, id`, kind)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Object, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		var o Object
		if err := rows.Scan(&o.ID, &o.Doc); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete removes (org, kind, id). Returns whether a row was deleted.
func (s *Store) Delete(ctx context.Context, org, kind, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM ingress_objects WHERE org=? AND kind=? AND id=?`, org, kind, id)
	if err != nil {
		return false, fmt.Errorf("delete %s/%s: %w", kind, id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PutTLS upserts the per-org ACME config document.
func (s *Store) PutTLS(ctx context.Context, org, doc string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ingress_tls (org,doc,updated_at) VALUES (?,?,?)
		 ON CONFLICT(org) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		org, doc, now)
	if err != nil {
		return fmt.Errorf("put tls: %w", err)
	}
	return nil
}

// GetTLS returns the per-org ACME config document.
func (s *Store) GetTLS(ctx context.Context, org string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT doc FROM ingress_tls WHERE org=?`, org)
	var doc string
	err := row.Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get tls: %w", err)
	}
	return doc, true, nil
}

// AllTLS returns every org's ACME config document — the union feeding the ACME
// HostPolicy's ExtraHosts set.
func (s *Store) AllTLS(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM ingress_tls`)
	if err != nil {
		return nil, fmt.Errorf("all tls: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("scan tls: %w", err)
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// isUniqueViolation reports a SQLite UNIQUE constraint failure across both the
// pure-Go modernc (CGO_ENABLED=0) and cgo mattn drivers, which both surface the
// canonical "UNIQUE constraint failed" text.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
