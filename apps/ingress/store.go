package ingress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on hanzo-sql the store is a schema of the system
	// namespace; without it the database is born encrypted under the key cek derives
	// from the process master and that namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags). Same driver
	// clients/settings and clients/eval use.
	_ "github.com/hanzoai/sqlite"
)

// The ingress STORE is one database (the deployment's own "ingress", a schema on
// hanzo-sql or a SQLite file) holding every org's edge config as opaque JSON
// documents keyed by (org, kind, id), in the dialect both engines share
// (sdk/dialect). Tenancy is the org column: every CRUD query binds
// `WHERE org=$1`, so one org can
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

// Store is the ingress metastore. On SQLite MaxOpenConns(1) serializes writes
// against the file lock (same discipline as settings/eval); on hanzo-sql a write
// that checks before it writes holds the store's write lock (Dialect.BeginWrite).
type Store struct {
	db *sql.DB
}

// ErrHostTaken is returned when a route Put would collide on the globally-unique
// host — another route (in any org) already claims it.
var ErrHostTaken = errors.New("host already claimed by another route")

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "ingress", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS ingress_objects (
  org        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  id         TEXT NOT NULL,
  doc        TEXT NOT NULL,
  host       TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (org, kind, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ingress_route_host_uniq
  ON ingress_objects(host) WHERE kind='route' AND host<>'';
CREATE TABLE IF NOT EXISTS ingress_tls (
  org        TEXT PRIMARY KEY,
  doc        TEXT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS ingress_acme (
  name       TEXT PRIMARY KEY,
  data       TEXT NOT NULL,
  updated_at BIGINT NOT NULL
);`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return dialect.Of(s.db).Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		return nil
	})
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Put upserts an object for (org, kind, id). host is the denormalized route host
// used for the global-uniqueness index ("" for non-route kinds). A collision on
// another route's host returns ErrHostTaken.
//
// The claim is asked first and written second, in one transaction under the store's
// write lock, so two replicas claiming one host take it in turn and the second is
// told. The partial unique index stays as the backstop; it is not how the claim is
// told apart from a failure, since each engine words that error its own way.
func (s *Store) Put(ctx context.Context, org, kind, id, doc, host string, now int64) error {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("put %s/%s: %w", kind, id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if kind == KindRoute && host != "" {
		var held int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM ingress_objects WHERE kind=$1 AND host=$2 AND NOT (org=$3 AND id=$4) LIMIT 1`,
			KindRoute, host, org, id).Scan(&held)
		if err == nil {
			return ErrHostTaken
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("put %s/%s: %w", kind, id, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ingress_objects (org,kind,id,doc,host,updated_at) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT(org,kind,id) DO UPDATE SET doc=excluded.doc, host=excluded.host, updated_at=excluded.updated_at`,
		org, kind, id, doc, host, now); err != nil {
		return fmt.Errorf("put %s/%s: %w", kind, id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put %s/%s: %w", kind, id, err)
	}
	return nil
}

// Get returns the object doc for (org, kind, id). found=false (nil error) when
// there is no row. Tenant isolation: the WHERE always binds org.
func (s *Store) Get(ctx context.Context, org, kind, id string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT doc FROM ingress_objects WHERE org=$1 AND kind=$2 AND id=$3`, org, kind, id)
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
	return s.query(ctx, `SELECT id, doc FROM ingress_objects WHERE org=$1 AND kind=$2 ORDER BY id`, org, kind)
}

// All returns every object of kind across ALL orgs — the input to the edge
// compile. It is a server-internal full-table read (NOT a tenant-scoped API
// call); the API handlers use List, which binds org.
func (s *Store) All(ctx context.Context, kind string) ([]Object, error) {
	return s.query(ctx, `SELECT id, doc FROM ingress_objects WHERE kind=$1 ORDER BY org, id`, kind)
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
		`DELETE FROM ingress_objects WHERE org=$1 AND kind=$2 AND id=$3`, org, kind, id)
	if err != nil {
		return false, fmt.Errorf("delete %s/%s: %w", kind, id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PutTLS upserts the per-org ACME config document.
func (s *Store) PutTLS(ctx context.Context, org, doc string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ingress_tls (org,doc,updated_at) VALUES ($1,$2,$3)
		 ON CONFLICT(org) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		org, doc, now)
	if err != nil {
		return fmt.Errorf("put tls: %w", err)
	}
	return nil
}

// GetTLS returns the per-org ACME config document.
func (s *Store) GetTLS(ctx context.Context, org string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT doc FROM ingress_tls WHERE org=$1`, org)
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
