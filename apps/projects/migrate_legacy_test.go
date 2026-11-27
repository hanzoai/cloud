package projects

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
	"github.com/hanzoai/cloud/sdk/locus"
	tenant "github.com/hanzoai/namespace"
)

// legacyProjectsDDL is the projects table BEFORE the cache_control/last_purge_at
// columns existed.
const legacyProjectsDDL = `
CREATE TABLE projects (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  slug            TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  repo_url        TEXT NOT NULL DEFAULT '',
  repo_branch     TEXT NOT NULL DEFAULT '',
  repo_provider   TEXT NOT NULL DEFAULT '',
  framework       TEXT NOT NULL DEFAULT 'static',
  status          TEXT NOT NULL DEFAULT 'draft',
  live_url        TEXT NOT NULL DEFAULT '',
  bucket          TEXT NOT NULL DEFAULT '',
  current_deploy  TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);`

// TestMigrateOverLegacyProjects locks that migrate() succeeds on a DB whose
// projects table predates the cache_control/last_purge_at columns.
//
// SQLite only, by nature: a legacy file is one an older binary made, and the harness
// seeds it as a file.
func TestMigrateOverLegacyProjects(t *testing.T) {
	t.Setenv(locus.Env, "")
	migratetest.Case{
		Name:      "projects",
		LegacyDDL: legacyProjectsDDL,
		Open: func(dir string) (io.Closer, error) {
			st, err := openStore(dir, testMaster, testKMS)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}

// A releases table made before releases carried their position is numbered by the
// order its rows were written, so the rollback menu lists them as it always did: newest
// written first, whatever their ids, even when they share a second. A release written
// by an older binary after that (its seq left at 0) is numbered the next time the store
// opens, behind the ones that were there.
//
// SQLite only, by nature: the order a file recorded was its rowid, which PostgreSQL has
// no counterpart of.
func TestAFileOfReleasesWithoutPositionKeepsItsOrder(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	raw, err := locus.Open(tenant.System(), subsystem, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE releases (
  id TEXT NOT NULL, org TEXT NOT NULL, slug TEXT NOT NULL, prefix TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '', objects INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, PRIMARY KEY (org, slug, id)
);
INSERT INTO releases (id, org, slug, prefix, created_at) VALUES
  ('zzz', 'acme', 'site', 'p/zzz', 100),
  ('aaa', 'acme', 'site', 'p/aaa', 100),
  ('mmm', 'acme', 'site', 'p/mmm', 100);`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = raw.Close()

	st, err := openStore(dir, testMaster, testKMS)
	if err != nil {
		t.Fatalf("open over the old file: %v", err)
	}
	ctx := context.Background()
	order := func(want ...string) {
		t.Helper()
		got, err := st.ListReleases(ctx, "acme", "site", 10)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var ids []string
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("releases = %v, want %v", ids, want)
		}
	}
	order("mmm", "aaa", "zzz")

	if err := st.PutRelease(ctx, Release{ID: "bbb", Org: "acme", Slug: "site", Prefix: "p/bbb", CreatedAt: 100}); err != nil {
		t.Fatalf("put: %v", err)
	}
	order("bbb", "mmm", "aaa", "zzz")

	// An older binary inserts without a position; the next open numbers it.
	if _, err := st.db.Exec(`INSERT INTO releases (id, org, slug, prefix, created_at) VALUES ('ccc', 'acme', 'site', 'p/ccc', 100)`); err != nil {
		t.Fatalf("older insert: %v", err)
	}
	if err := st.migrate(); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	order("ccc", "bbb", "mmm", "aaa", "zzz")
	_ = st.Close()
}
