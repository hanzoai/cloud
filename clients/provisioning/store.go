package provisioning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
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
	// Size is the declared storage footprint of a DEDICATED-instance resource
	// (e.g. "10Gi"), empty for the shared-logical kinds. It is the live-size
	// source the recurring footprint meter multiplies into a per-org GB-time
	// charge — so an instance the org runs is billed for what it reserves.
	Size string
	// Instance binds a DEDICATED-instance resource to the app instance whose
	// on-demand add-on it is (e.g. "commerce"). When set, the assembled DSN is
	// injected as <KIND>_URL into the Secret "<instance>-addons" in tenant-<org>,
	// so that instance switches off Base onto this backend; drop removes it and
	// the instance reverts to Base. Empty = not instance-bound (the DSN is only
	// returned once, wired by the caller) — the pre-instance-binding behavior, so
	// every existing provision is unchanged.
	Instance string
}

// Store is the provisioning metadata database. ONE SQLite file
// ({DataDir}/provisioning.db) holds every org's records; tenant isolation is
// by the org column, enforced at the query layer. MaxOpenConns(1) serializes
// access so multi-step writes never race the SQLite file lock.
type Store struct {
	db *sql.DB
}

// openStore opens (creating if needed) the SQLite metadata DB at path and runs
// the migration. The "sqlite" driver is the hanzoai/sqlite fork.
func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
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
-- Global (cross-org) physical-name uniqueness: the authoritative guard that
-- two distinct logical resources can NEVER map onto one physical backend
-- resource. physical_name embeds a fixed-width org hash, so this also pins
-- cross-tenant isolation at the physical layer (not just UNIQUE(org,kind,name),
-- which two distinct rows could satisfy while colliding physically). The row
-- itself maps physical_name -> (org,kind,name) so names stay traceable.
CREATE UNIQUE INDEX IF NOT EXISTS ux_provisioned_physical
  ON provisioned_resources(physical_name);
CREATE INDEX IF NOT EXISTS ix_provisioned_org_kind_rank
  ON provisioned_resources(org, kind, created_at);
-- Cross-org status scan for the recurring footprint meter (list every ready
-- dedicated instance to charge its org).
CREATE INDEX IF NOT EXISTS ix_provisioned_status
  ON provisioned_resources(status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Additive column for the dedicated-instance size dimension. CREATE TABLE
	// IF NOT EXISTS never alters an existing table, so add the column
	// idempotently — a "duplicate column name" on an already-migrated DB is the
	// expected no-op, any other error is real.
	if _, err := s.db.Exec(`ALTER TABLE provisioned_resources ADD COLUMN size TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate size column: %w", err)
	}
	// Additive column for the app-instance binding dimension (the on-demand
	// add-on target). Same idempotent ALTER pattern as `size`: a "duplicate
	// column name" on an already-migrated DB is the expected no-op.
	if _, err := s.db.Exec(`ALTER TABLE provisioned_resources ADD COLUMN instance TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate instance column: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const resourceCols = `id,org,kind,name,physical_name,secret_ref,host,port,username,dbname,status,created_at,size,instance`

func scanResource(sc interface{ Scan(...any) error }) (Resource, error) {
	var r Resource
	err := sc.Scan(&r.ID, &r.Org, &r.Kind, &r.Name, &r.PhysicalName, &r.SecretRef,
		&r.Host, &r.Port, &r.Username, &r.DBName, &r.Status, &r.CreatedAt, &r.Size, &r.Instance)
	return r, err
}

// Insert writes one resource row inside a transaction. A UNIQUE(org,kind,name)
// OR UNIQUE(physical_name) violation surfaces as errConflict so the caller can
// roll back the backend side-effects it already performed.
func (s *Store) Insert(ctx context.Context, r Resource) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO provisioned_resources (`+resourceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.Kind, r.Name, r.PhysicalName, r.SecretRef,
		r.Host, r.Port, r.Username, r.DBName, r.Status, r.CreatedAt, r.Size, r.Instance)
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

// PhysicalExists reports whether ANY org already owns the given physical
// backend name. This is the global (cross-org) uniqueness pre-check: paired
// with the UNIQUE(physical_name) index it lets the handler fail closed with 409
// BEFORE it touches a backend, so a residual name-fold (or hash collision) can
// never silently provision over another tenant's physical resource.
func (s *Store) PhysicalExists(ctx context.Context, physical string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM provisioned_resources WHERE physical_name=? LIMIT 1`, physical).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("physical exists: %w", err)
	}
	return true, nil
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

// ListByInstance returns every resource an org has bound to one app instance,
// oldest first — the set of on-demand add-ons active for that instance (each is
// one <KIND>_URL projected into the instance's addons Secret). Scoped to (org,
// instance) so it can never surface another tenant's bindings.
func (s *Store) ListByInstance(ctx context.Context, org, instance string) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+resourceCols+` FROM provisioned_resources WHERE org=? AND instance=? ORDER BY created_at ASC, id ASC`,
		org, instance)
	if err != nil {
		return nil, fmt.Errorf("list by instance: %w", err)
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

// UpdateStatus advances one resource's status (e.g. dedicated "provisioning" ->
// "ready" once the operator reports the instance is up). Scoped to (org,kind,
// name) so it can never touch another tenant's row. Reports whether a row moved.
func (s *Store) UpdateStatus(ctx context.Context, org, kind, name, status string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE provisioned_resources SET status=? WHERE org=? AND kind=? AND name=?`,
		status, org, kind, name)
	if err != nil {
		return false, fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListAllByStatus returns every row (across ALL orgs) in the given status. It is
// the read side of the recurring footprint meter: the sweep lists every "ready"
// row and charges each dedicated instance's OWN org. Rows carry Org/Kind/Size so
// the caller attributes and prices without a second lookup.
func (s *Store) ListAllByStatus(ctx context.Context, status string) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+resourceCols+` FROM provisioned_resources WHERE status=? ORDER BY org ASC, kind ASC, name ASC`,
		status)
	if err != nil {
		return nil, fmt.Errorf("list by status: %w", err)
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
