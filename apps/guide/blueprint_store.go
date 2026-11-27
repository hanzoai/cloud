package guide

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Open is the ONE opener: it names the database in the system
	// namespace, opens it encrypted under the key cek derives from the process
	// master (or as a schema on hanzo-sql), and applies the single-connection cap
	// the SQLite store's atomicity rests on.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// blueprint_store.go is the SHARED, platform-scoped store for the brand blueprint —
// the middle resolution tier a SuperAdmin authors on admin.hanzo.ai. Unlike the per-org
// store (store.go, one physical file per org), this is ONE file for the whole
// deployment: the brand blueprint is SHARED platform content (like an always-on
// schema), keyed by brand, NOT by tenant. Isolation is by AUTHORITY (SuperAdmin only,
// enforced at the admin plane in admin.go), never by org.
//
// Versioning mirrors the legal-template discipline: every author write appends a new
// version; the seed is version 1; the latest version is authoritative. Old versions
// stay for point-in-time recovery / audit.
//
// Provenance is EXPLICIT, not inferred from version number: every row carries a `source`
// ("seed" | "admin") and the seed row carries the `seed_version` generation it was seeded
// from. The seed occupies version 1 with source="seed"; an admin write (SaveVersion)
// appends version >= 2 with source="admin". This is what makes the seed VERSION-AWARE
// (SeedOrUpgrade): on Mount a brand with NO row is seeded; a brand whose seed is still
// UNEDITED (no source="admin" row) and older than the embedded seedVersion is UPGRADED in
// place; a brand that was EVER admin-edited is NEVER touched — the load-bearing invariant.

// BlueprintStore persists brand blueprints as versioned canonical-JSON docs.
type BlueprintStore struct {
	db *sql.DB
}

// openBlueprintStore opens the shared blueprint DB under dir and migrates. It is
// the DEPLOYMENT's own partition — namespace.System takes no input and names no
// entity — because a brand blueprint is platform content, not an org's.
// On SQLite MaxOpenConns(1) serializes writes.
func openBlueprintStore(dir string) (*BlueprintStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "guide-blueprint", dir, func(db *sql.DB) (*BlueprintStore, error) {
		s := &BlueprintStore{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *BlueprintStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS guide_blueprint (
  brand      TEXT NOT NULL,
  version    BIGINT NOT NULL,
  doc        TEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (brand, version)
);`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		// Add the provenance columns (version-aware seed) if this DB predates them.
		// There is no ADD COLUMN IF NOT EXISTS both engines read, so gate on the live schema.
		for _, c := range []struct{ name, ddl string }{
			{"source", "TEXT NOT NULL DEFAULT 'seed'"},
			{"seed_version", "BIGINT NOT NULL DEFAULT 0"},
		} {
			has, err := d.HasColumn(ctx, tx, "guide_blueprint", c.name)
			if err != nil {
				return err
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE guide_blueprint ADD COLUMN `+c.name+` `+c.ddl); err != nil {
				return fmt.Errorf("add %s: %w", c.name, err)
			}
		}
		// Enforce the provenance invariant idempotently: a version >= 2 row is an admin edit (the
		// seed always occupies version 1). This backfills a LEGACY row that predates the source
		// column — created by the old SaveVersion, defaulted to 'seed' — to 'admin', so a legacy
		// admin edit is never mistaken for an upgradeable seed. The `AND source='seed'` guard
		// makes it a no-op once every version>=2 row is 'admin' (self-healing, no false flips: a
		// seed upgrade keeps version 1, and every new admin write is stamped 'admin' at write).
		_, err := tx.ExecContext(ctx, `UPDATE guide_blueprint SET source='admin' WHERE version >= 2 AND source='seed'`)
		return err
	}); err != nil {
		return fmt.Errorf("guide blueprint migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *BlueprintStore) Close() error { return s.db.Close() }

// docFunc produces the seed document, and it is a FUNCTION because producing one is
// expensive and usually unnecessary. The embedded fixture is 1.2 MB of YAML to parse
// and a JSON document to marshal, while the answer on a boot that already holds a
// current seed is "write nothing" — decided by two indexed reads above. Passing the
// bytes made every process pay for a document the common case discards; passing the
// means of making them lets the store ask first.
type docFunc func() ([]byte, error)

// bytesDoc adapts a document already in hand, for a caller that has one.
func bytesDoc(b []byte) docFunc { return func() ([]byte, error) { return b, nil } }

// SeedAction is the outcome of a SeedOrUpgrade call (for logging / test assertions).
type SeedAction string

const (
	SeedNone     SeedAction = "unchanged" // admin-edited, or seed already current
	SeedInserted SeedAction = "seeded"    // no row existed → fresh seed at version 1
	SeedUpgraded SeedAction = "upgraded"  // unedited seed replaced with a newer generation
)

// SeedOrUpgrade is the VERSION-AWARE seed — the ONE way the embedded fixture reaches the
// DB. Given the embedded doc + its monotonic seedVersion, it:
//
//   - NEVER touches a brand that carries an admin edit (source="admin") — the hard
//     invariant: any admin edit at any version survives forever (returns SeedNone);
//   - seeds version 1 (source="seed", stamped seedVersion) when the brand has no row;
//   - UPGRADES the existing UNEDITED seed in place (same version 1) when the embedded
//     seedVersion is strictly newer than the stored one — so a deployment seeded with an
//     older corpus picks up new defaults on redeploy;
//   - otherwise is a no-op (the seed is already at this generation).
//
// Idempotent: re-running with the same seedVersion over an already-current seed is SeedNone.
func (s *BlueprintStore) SeedOrUpgrade(ctx context.Context, brand string, doc docFunc, seedVersion int, now int64) (SeedAction, error) {
	// 1. An admin edit makes the brand off-limits to the seeder, forever.
	var adminN int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM guide_blueprint WHERE brand=$1 AND source='admin'`, brand).Scan(&adminN); err != nil {
		return SeedNone, fmt.Errorf("seed check admin: %w", err)
	}
	if adminN > 0 {
		return SeedNone, nil
	}
	// 2. The unedited seed row (there is at most one, at version 1) and its generation.
	var stored sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT seed_version FROM guide_blueprint WHERE brand=$1 AND source='seed' ORDER BY version LIMIT 1`, brand).
		Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		// Atomic insert-if-absent — no TOCTOU with a concurrent seeder (a second replica over
		// a shared file): the WHERE NOT EXISTS lets exactly one writer win; the loser is a
		// no-op, never a primary-key conflict.
		body, err := doc()
		if err != nil {
			return SeedNone, err
		}
		if err := dialect.Text(brand, string(body)); err != nil {
			return SeedNone, fmt.Errorf("seed blueprint: %w", err)
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO guide_blueprint (brand, version, doc, updated_at, source, seed_version)
			 SELECT CAST($1 AS TEXT), 1, CAST($2 AS TEXT), CAST($3 AS BIGINT), 'seed', CAST($4 AS BIGINT)
			 WHERE NOT EXISTS (SELECT 1 FROM guide_blueprint WHERE brand=$5)
			 ON CONFLICT (brand, version) DO NOTHING`,
			brand, string(body), now, seedVersion, brand)
		if err != nil {
			return SeedNone, fmt.Errorf("seed blueprint: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return SeedInserted, nil
		}
		return SeedNone, nil // a concurrent writer seeded first
	}
	if err != nil {
		return SeedNone, fmt.Errorf("seed check version: %w", err)
	}
	// 3. Unedited seed present → upgrade in place ONLY when the embedded generation is newer.
	if seedVersion > int(stored.Int64) {
		body, err := doc()
		if err != nil {
			return SeedNone, err
		}
		if err := dialect.Text(string(body)); err != nil {
			return SeedNone, fmt.Errorf("upgrade seed: %w", err)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE guide_blueprint SET doc=$1, seed_version=$2, updated_at=$3 WHERE brand=$4 AND source='seed'`,
			string(body), seedVersion, now, brand); err != nil {
			return SeedNone, fmt.Errorf("upgrade seed: %w", err)
		}
		return SeedUpgraded, nil
	}
	return SeedNone, nil
}

// SaveVersion appends a new admin version for brand (max version + 1) and returns it. This
// is the author write — every edit is a new immutable version (point-in-time recovery),
// stamped source="admin" so SeedOrUpgrade will never clobber it. seed_version is 0 on an
// admin row (the field is meaningful only for the seed).
func (s *BlueprintStore) SaveVersion(ctx context.Context, brand string, doc []byte, now int64) (int, error) {
	if err := dialect.Text(brand, string(doc)); err != nil {
		return 0, fmt.Errorf("save blueprint version: %w", err)
	}
	// The next version is read, then written: under the store's one write lock, so two
	// authors cannot both take it.
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return 0, fmt.Errorf("save blueprint version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var max sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM guide_blueprint WHERE brand=$1`, brand).Scan(&max); err != nil {
		return 0, fmt.Errorf("next blueprint version: %w", err)
	}
	version := 1
	if max.Valid {
		version = int(max.Int64) + 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO guide_blueprint (brand, version, doc, updated_at, source, seed_version) VALUES ($1,$2,$3,$4,'admin',0)`,
		brand, version, string(doc), now); err != nil {
		return 0, fmt.Errorf("save blueprint version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("save blueprint version: %w", err)
	}
	return version, nil
}

// latestForBrand returns the latest doc+version for exactly brand, or ok=false.
func (s *BlueprintStore) latestForBrand(ctx context.Context, brand string) (doc []byte, version int, ok bool, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx,
		`SELECT doc, version FROM guide_blueprint WHERE brand=$1 ORDER BY version DESC LIMIT 1`, brand).
		Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("latest blueprint: %w", err)
	}
	return []byte(raw), version, true, nil
}

// LatestResolved returns the latest blueprint doc for a deployment's brand, falling
// back to the base ("") blueprint, and reports WHICH brand key it resolved. Reads and
// admin writes both key off this SAME resolution, so a deployment authors exactly the
// row it serves (a brand with its own seeded row edits that row; a brand with none —
// e.g. "hanzo", which shares the base — edits ""). ok=false only when the DB carries
// nothing (pre-seed / unreachable), where the caller falls through to the fixture.
func (s *BlueprintStore) LatestResolved(ctx context.Context, brand string) (doc []byte, version int, key string, ok bool, err error) {
	if brand != "" {
		if doc, version, ok, err = s.latestForBrand(ctx, brand); err != nil || ok {
			return doc, version, brand, ok, err
		}
	}
	doc, version, ok, err = s.latestForBrand(ctx, "")
	return doc, version, "", ok, err
}

// VersionMeta is one stored version's metadata (audit / PITR listing — never the full
// doc, which the GET returns for the active version).
type VersionMeta struct {
	// Brand is the white-label key this revision was authored under; empty is the
	// shared base playbook. Revisions of two brands never share a number line.
	Brand string `json:"brand"`
	// Version is the store's own revision counter for that brand, starting at 1 for
	// the seeded playbook and incrementing on every edit. Nothing is overwritten, so
	// the highest number is the live one and every lower number is still readable.
	// It is not the playbook's authored `version` string.
	Version int `json:"version"`
	// UpdatedAt is when this revision was written, as Unix seconds — the "who
	// changed the playbook, and when" half of the audit trail.
	UpdatedAt int64 `json:"updatedAt"`
}

// ListVersions returns brand's versions, newest first — the PITR/audit trail.
func (s *BlueprintStore) ListVersions(ctx context.Context, brand string) ([]VersionMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT brand, version, updated_at FROM guide_blueprint WHERE brand=$1 ORDER BY version DESC`, brand)
	if err != nil {
		return nil, fmt.Errorf("list blueprint versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]VersionMeta, 0, 8)
	for rows.Next() {
		var m VersionMeta
		if err := rows.Scan(&m.Brand, &m.Version, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
