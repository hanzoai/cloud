package provisioningsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver (already in the cloud
	// dep graph via graph). Blank import registers the "sqlite" driver name.
	_ "modernc.org/sqlite"
)

// errConflict is returned by Insert when the (org,kind,name) tuple already
// exists. errNotFound is returned by Get when no row matches. Callers map
// these to HTTP 409 / 404.
var (
	errConflict = errors.New("provisioning: resource already exists")
	errNotFound = errors.New("provisioning: resource not found")
)

// Resource is one row of provisioned_resources: the control-plane record for a
// logical resource (database, bucket, collection, …) created inside a shared
// backend. It never carries the plaintext password — only secret_ref, the KMS
// key under which the password is sealed.
type Resource struct {
	ID           string
	Org          string
	Kind         string
	Name         string
	PhysicalName string
	SecretRef    string
	Host         string
	Port         int
	Username     string
	DBName       string
	Status       string
	CreatedAt    int64
}

// Store is the provisioning metadata database. ONE SQLite file
// ({DataDir}/provisioning.db) holds every org's records; tenant isolation is
// by the org column, enforced at the query layer. MaxOpenConns(1) serializes
// access so multi-step writes never race the SQLite file lock.
type Store struct {
	db *sql.DB
}

// openStore opens (creating if needed) the SQLite metadata DB at path and runs
// the migration. The "sqlite" driver is modernc's pure-Go build.
func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// Single connection: the control-plane table is low-volume and this makes
	// every write atomic against the file lock without busy-loop retries.
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
CREATE TABLE IF NOT EXISTS provisioned_resources (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  kind          TEXT NOT NULL,
  name          TEXT NOT NULL,
  physical_name TEXT NOT NULL,
  secret_ref    TEXT NOT NULL DEFAULT '',
  host          TEXT NOT NULL DEFAULT '',
  port          INTEGER NOT NULL DEFAULT 0,
  username      TEXT NOT NULL DEFAULT '',
  dbname        TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_provisioned_org_kind_name
  ON provisioned_resources(org, kind, name);
CREATE INDEX IF NOT EXISTS ix_provisioned_org_kind_rank
  ON provisioned_resources(org, kind, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const resourceCols = `id,org,kind,name,physical_name,secret_ref,host,port,username,dbname,status,created_at`

func scanResource(sc interface{ Scan(...any) error }) (Resource, error) {
	var r Resource
	err := sc.Scan(&r.ID, &r.Org, &r.Kind, &r.Name, &r.PhysicalName, &r.SecretRef,
		&r.Host, &r.Port, &r.Username, &r.DBName, &r.Status, &r.CreatedAt)
	return r, err
}

// Insert writes one resource row inside a transaction. A UNIQUE(org,kind,name)
// violation surfaces as errConflict so the caller can roll back the backend
// side-effects it already performed.
func (s *Store) Insert(ctx context.Context, r Resource) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO provisioned_resources (`+resourceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.Kind, r.Name, r.PhysicalName, r.SecretRef,
		r.Host, r.Port, r.Username, r.DBName, r.Status, r.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert: %w", err)
	}
	return tx.Commit()
}

// Get returns the resource for (org,kind,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, kind, name string) (Resource, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+resourceCols+` FROM provisioned_resources WHERE org=? AND kind=? AND name=?`,
		org, kind, name)
	r, err := scanResource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, errNotFound
	}
	if err != nil {
		return Resource{}, fmt.Errorf("get: %w", err)
	}
	return r, nil
}

// List returns every resource of kind for org, oldest first.
func (s *Store) List(ctx context.Context, org, kind string) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+resourceCols+` FROM provisioned_resources WHERE org=? AND kind=? ORDER BY created_at ASC, id ASC`,
		org, kind)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Resource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes the resource row inside a transaction. Reports whether a row
// was actually deleted.
func (s *Store) Delete(ctx context.Context, org, kind, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM provisioned_resources WHERE org=? AND kind=? AND name=?`,
		org, kind, name)
	if err != nil {
		return false, fmt.Errorf("delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}
