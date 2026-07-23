package guide

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/cek"
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
// stay for point-in-time recovery / audit. A redeploy re-seeds ONLY when a brand has no
// row (seed-if-absent), so admin edits (version >= 2) are never clobbered.

// BlueprintStore persists brand blueprints as versioned canonical-JSON docs.
type BlueprintStore struct {
	db *sql.DB
}

// openBlueprintStore opens the shared blueprint DB at path (a cek-sealed SQLite file,
// the house pattern) and migrates. MaxOpenConns(1) serializes writes.
func openBlueprintStore(path string) (*BlueprintStore, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open blueprint store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &BlueprintStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *BlueprintStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS guide_blueprint (
  brand      TEXT NOT NULL,
  version    INTEGER NOT NULL,
  doc        TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (brand, version)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("guide blueprint migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *BlueprintStore) Close() error { return s.db.Close() }

// hasBrand reports whether ANY version exists for brand — the seed-if-absent predicate.
func (s *BlueprintStore) hasBrand(ctx context.Context, brand string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM guide_blueprint WHERE brand=? LIMIT 1`, brand).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has brand: %w", err)
	}
	return true, nil
}

// SeedIfAbsent inserts doc as version 1 for brand IFF no row exists for that brand. It
// is idempotent: a redeploy over an already-seeded (or admin-edited) brand is a no-op,
// so an admin's version-2+ edits are never clobbered. Returns whether it seeded.
func (s *BlueprintStore) SeedIfAbsent(ctx context.Context, brand string, doc []byte, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO guide_blueprint (brand, version, doc, updated_at)
		 SELECT ?, 1, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM guide_blueprint WHERE brand=?)`,
		brand, string(doc), now, brand)
	if err != nil {
		return false, fmt.Errorf("seed blueprint: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SaveVersion appends a new version for brand (max version + 1) and returns it. This is
// the author write — every edit is a new immutable version (point-in-time recovery).
func (s *BlueprintStore) SaveVersion(ctx context.Context, brand string, doc []byte, now int64) (int, error) {
	var max sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM guide_blueprint WHERE brand=?`, brand).Scan(&max); err != nil {
		return 0, fmt.Errorf("next blueprint version: %w", err)
	}
	version := 1
	if max.Valid {
		version = int(max.Int64) + 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO guide_blueprint (brand, version, doc, updated_at) VALUES (?,?,?,?)`,
		brand, version, string(doc), now); err != nil {
		return 0, fmt.Errorf("save blueprint version: %w", err)
	}
	return version, nil
}

// latestForBrand returns the latest doc+version for exactly brand, or ok=false.
func (s *BlueprintStore) latestForBrand(ctx context.Context, brand string) (doc []byte, version int, ok bool, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx,
		`SELECT doc, version FROM guide_blueprint WHERE brand=? ORDER BY version DESC LIMIT 1`, brand).
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
	Brand     string `json:"brand"`
	Version   int    `json:"version"`
	UpdatedAt int64  `json:"updatedAt"`
}

// ListVersions returns brand's versions, newest first — the PITR/audit trail.
func (s *BlueprintStore) ListVersions(ctx context.Context, brand string) ([]VersionMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT brand, version, updated_at FROM guide_blueprint WHERE brand=? ORDER BY version DESC`, brand)
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
