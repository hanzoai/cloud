package entitlements

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	"github.com/hanzoai/cloud/internal/cek"
	_ "github.com/hanzoai/sqlite"
)

// The entitlements STORE is the durable record of WHICH canonical products an
// org has ENABLED — the source of truth the console's paid-product sidebar reads
// to show/hide a product. It follows the settings-store discipline verbatim:
// Hanzo Base/SQLite, MaxOpenConns(1) to serialize writes against the file lock,
// ONE file for every org.
//
// A ROW EXISTS iff the product is enabled for that org. Enabling inserts the row;
// disabling deletes it. GET is the SELECT of every product for the org.
//
// Org isolation is the (org, product) COMPOSITE PRIMARY KEY and a mandatory
// `WHERE org=?` on EVERY statement. The org value is the VALIDATED owner claim
// (principal.Org / the :org param a super admin targets) — never normalized
// (casing/trimming collapses distinct owners into one bucket) and never a
// client-supplied body field.
//
// This store records ENABLEMENT (org intent), NOT ENTITLEMENT (what the plan
// grants). Entitlement is commerce's authority, checked at write time before a
// paid product may be enabled. The two are deliberately separate: enablement is
// the visibility toggle, entitlement is the billing gate.

// Store is the entitlements metastore over one SQLite file
// ({DataDir}/entitlements.db). Org-scoping is the (org, product) key.
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
CREATE TABLE IF NOT EXISTS entitlements (
  org        TEXT NOT NULL,
  product    TEXT NOT NULL,
  enabled_at INTEGER NOT NULL,
  enabled_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org, product)
);
CREATE INDEX IF NOT EXISTS ix_entitlements_org ON entitlements(org);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// List returns the enabled product ids for org, sorted (a stable contract the
// console can diff). An org that has enabled nothing returns an empty slice, not
// an error — "no products enabled yet" is a valid state, never a 404.
func (s *Store) List(ctx context.Context, org string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT product FROM entitlements WHERE org=? ORDER BY product`, org)
	if err != nil {
		return nil, fmt.Errorf("list entitlements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan entitlement: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entitlements: %w", err)
	}
	return out, nil
}

// Enable turns product on for org (idempotent — re-enabling keeps the original
// enabled_at, refreshing only who last enabled it). `by` is the actor's user id,
// recorded for audit; empty is allowed (e.g. a super-admin machine grant).
func (s *Store) Enable(ctx context.Context, org, product, by string, atUnix int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO entitlements (org, product, enabled_at, enabled_by)
		 VALUES (?,?,?,?)
		 ON CONFLICT(org, product) DO UPDATE SET enabled_by=excluded.enabled_by`,
		org, product, atUnix, by)
	if err != nil {
		return fmt.Errorf("enable entitlement: %w", err)
	}
	return nil
}

// Disable turns product off for org (idempotent — disabling an already-off
// product is a no-op, never an error).
func (s *Store) Disable(ctx context.Context, org, product string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM entitlements WHERE org=? AND product=?`, org, product)
	if err != nil {
		return fmt.Errorf("disable entitlement: %w", err)
	}
	return nil
}

// Apply enables `add` and disables `remove` for org in ONE transaction, so a
// batch mutation is all-or-nothing (the console never sees a half-applied set).
// It returns the resulting enabled set. add/remove are already product-shape
// validated and entitlement-gated by the caller; a product in BOTH lists is
// removed (remove wins — the explicit "off" is the safer resolution).
func (s *Store) Apply(ctx context.Context, org string, add, remove []string, by string, atUnix int64) ([]string, error) {
	removeSet := make(map[string]struct{}, len(remove))
	for _, p := range remove {
		removeSet[p] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range add {
		if _, dup := removeSet[p]; dup {
			continue // remove wins; do not enable a product also being removed
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entitlements (org, product, enabled_at, enabled_by)
			 VALUES (?,?,?,?)
			 ON CONFLICT(org, product) DO UPDATE SET enabled_by=excluded.enabled_by`,
			org, p, atUnix, by); err != nil {
			return nil, fmt.Errorf("enable %q: %w", p, err)
		}
	}
	for p := range removeSet {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM entitlements WHERE org=? AND product=?`, org, p); err != nil {
			return nil, fmt.Errorf("disable %q: %w", p, err)
		}
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT product FROM entitlements WHERE org=? ORDER BY product`, org)
	if err != nil {
		return nil, fmt.Errorf("read-back: %w", err)
	}
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan read-back: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate read-back: %w", err)
	}
	_ = rows.Close()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	sort.Strings(out)
	return out, nil
}
